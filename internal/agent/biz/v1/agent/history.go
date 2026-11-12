// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/agent/pkg/metrics"

	"github.com/onexstack/onexai/internal/agent/pkg/tokenizer"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

const (
	// summaryChunkTokens bounds the input of a single summarization call. The
	// served model's window is far larger, but chunking is what makes
	// compaction work at any history size: the summarizer is itself a model
	// call, so feeding it a context that already exceeds the window would fail
	// exactly the way the run it is trying to rescue would have.
	summaryChunkTokens = 200000

	// summaryTimeout bounds one summarization call.
	summaryTimeout = 30 * time.Second

	// minPreservedMessages is the floor on messages kept verbatim, so a
	// pathological budget can never compress away the most recent exchange.
	minPreservedMessages = 4

	// summaryMarker prefixes the synthesized message so both the model and a
	// human reading the transcript can tell it apart from a real turn.
	summaryMarker = "【历史对话概要（由AI自动压缩归档）】\n"
)

// shrinkHistory bounds the conversation history a turn is about to send, and
// persists the compacted form so the cost is paid once rather than every turn.
//
// incoming is the turn's own user message. It counts against the budget but is
// not part of the returned history: the caller appends it, and the run
// persists it on completion, so including it here would duplicate it.
//
// On any failure it degrades to a deterministic prune, including when no
// summarizer is configured at all. A summarization call can fail the same ways
// a run can (timeout, rate limit, provider error), and without that fallback a
// failed summary would turn one 400 into another.
//
// Setting MaxContextTokens to zero is the only way to opt out.
func (b *AgentBiz) shrinkHistory(ctx context.Context, key string, history []agentapi.Message, incoming agentapi.Message) []agentapi.Message {
	if len(history) == 0 || b.deps.MaxContextTokens <= 0 {
		return history
	}

	counts, err := b.countEach(history)
	if err != nil {
		slog.WarnContext(ctx, "could not count session history, leaving it uncompacted", "error", err)
		return history
	}
	incomingTokens, err := b.countMessages([]agentapi.Message{incoming})
	if err != nil {
		slog.WarnContext(ctx, "could not count incoming message, leaving history uncompacted", "error", err)
		return history
	}

	total := sum(counts) + incomingTokens
	if total <= b.deps.MaxContextTokens {
		return history
	}

	preserve := b.deps.ContextPreserveTokens
	if preserve <= 0 {
		preserve = b.deps.MaxContextTokens / 3
	}

	cut := cutForPreserve(history, counts, preserve)
	old, keep := history[:cut], history[cut:]

	var compacted []agentapi.Message
	if len(old) > 0 {
		slog.InfoContext(ctx, "session history exceeds context budget, compacting",
			"session", key,
			"total_tokens", total,
			"budget_tokens", b.deps.MaxContextTokens,
			"compressed_messages", len(old),
			"preserved_messages", len(keep),
		)

		var ok bool
		if compacted, ok = b.summarizeOldest(ctx, old, keep); ok {
			metrics.M.ObserveCompaction("summarized")
		} else {
			// The fallback is not equivalent to the summary — it drops the
			// stretch rather than condensing it — so the two are counted apart.
			metrics.M.ObserveCompaction("pruned")
			compacted = pruneToBudget(history, counts, b.deps.MaxContextTokens)
		}
	} else {
		// There is nothing older than the preserved window to condense. Asking
		// the model to summarize an empty stretch does not fail — it returns a
		// summary of nothing, which then gets prepended and persisted, growing
		// the history this function exists to shrink and derailing the turns
		// that follow. Trim deterministically instead; the floor is a
		// preference, not a guarantee, so this still bounds a preserved window
		// that is itself over budget.
		compacted = pruneToBudget(history, counts, b.deps.MaxContextTokens)
		if len(compacted) == len(history) {
			// The history fits on its own; this turn's own message is what
			// overflows, and only the in-run guard can bound that.
			return history
		}
		slog.InfoContext(ctx, "session history exceeds context budget, trimming the preserved window",
			"session", key,
			"total_tokens", total,
			"budget_tokens", b.deps.MaxContextTokens,
			"trimmed_messages", len(history)-len(compacted),
		)
	}

	if err := b.deps.Sessions.Save(ctx, key, compacted); err != nil {
		slog.WarnContext(ctx, "could not persist compacted history, the next turn will compact again",
			"session", key, "error", err)
	}
	return compacted
}

// summarizeOldest condenses msgs into one archival message placed ahead of
// keep. It reports false when summarization failed, leaving the caller to fall
// back to pruning.
func (b *AgentBiz) summarizeOldest(ctx context.Context, msgs, keep []agentapi.Message) ([]agentapi.Message, bool) {
	summary, err := b.summarizeChunked(ctx, msgs)
	if err != nil {
		slog.WarnContext(ctx, "summarizing session history failed, falling back to pruning", "error", err)
		return nil, false
	}

	out := make([]agentapi.Message, 0, len(keep)+1)
	// The summary is a user message, not a system one: toSchemaMessages drops
	// system-role history because the agent instruction already carries the
	// system prompt, so a system-role summary would never reach the model.
	out = append(out, agentapi.Message{Role: agentapi.RoleUser, Content: summaryMarker + summary})
	return append(out, keep...), true
}

// summarizeChunked condenses msgs, splitting them so no single summarization
// call is handed more than summaryChunkTokens.
func (b *AgentBiz) summarizeChunked(ctx context.Context, msgs []agentapi.Message) (string, error) {
	chunks, err := b.chunkByTokens(msgs, summaryChunkTokens)
	if err != nil {
		return "", err
	}

	if len(chunks) == 1 {
		return b.summarizeOnce(ctx, chunks[0])
	}

	parts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		part, err := b.summarizeOnce(ctx, chunk)
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
	}

	// Fold the partial summaries if the assembled text is itself large enough
	// to be worth condensing again.
	joined := strings.Join(parts, "\n\n")
	if tokens, err := b.countMessages([]agentapi.Message{{Role: agentapi.RoleAssistant, Content: joined}}); err == nil &&
		tokens > summaryChunkTokens {
		folded, err := b.summarizeOnce(ctx, []agentapi.Message{{Role: agentapi.RoleAssistant, Content: joined}})
		if err != nil {
			return "", err
		}
		return folded, nil
	}
	return joined, nil
}

// summarizeOnce runs one summarization call under its own timeout. It is the
// single choke point for summarization, so the guard against an unconfigured
// summarizer lives here and every caller degrades to pruning uniformly.
func (b *AgentBiz) summarizeOnce(ctx context.Context, msgs []agentapi.Message) (string, error) {
	if b.deps.Summarize == nil {
		return "", fmt.Errorf("summarize %d messages: no summarizer is configured", len(msgs))
	}

	callCtx, cancel := context.WithTimeout(ctx, summaryTimeout)
	defer cancel()

	summary, err := b.deps.Summarize(callCtx, msgs)
	if err != nil {
		return "", fmt.Errorf("summarize %d messages: %w", len(msgs), err)
	}
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("summarize %d messages: model returned an empty summary", len(msgs))
	}
	return summary, nil
}

// chunkByTokens groups msgs into runs of at most limit tokens, preferring to
// break at a turn start so a chunk does not begin mid-exchange.
func (b *AgentBiz) chunkByTokens(msgs []agentapi.Message, limit int) ([][]agentapi.Message, error) {
	counts, err := b.countEach(msgs)
	if err != nil {
		return nil, err
	}

	var (
		chunks  [][]agentapi.Message
		current []agentapi.Message
		running int
	)
	flush := func() {
		if len(current) > 0 {
			chunks = append(chunks, current)
			current = nil
			running = 0
		}
	}

	for i, m := range msgs {
		// Break before a turn start once the chunk is already carrying content,
		// but never emit a chunk that is empty because a single turn is huge.
		if running > 0 && m.Role == agentapi.RoleUser && running+counts[i] > limit {
			flush()
		}
		current = append(current, m)
		running += counts[i]
		// A single oversized message is unavoidable; it becomes its own chunk.
		if running >= limit {
			flush()
		}
	}
	flush()

	if len(chunks) == 0 {
		chunks = append(chunks, msgs)
	}
	return chunks, nil
}

// cutForPreserve returns the index at which history should be split so the tail
// keeps roughly preserveTokens. The cut lands on a turn start and never trims
// below minPreservedMessages messages.
func cutForPreserve(history []agentapi.Message, counts []int, preserveTokens int) int {
	cut, running := len(history), 0
	for i := len(history) - 1; i >= 0; i-- {
		if running+counts[i] > preserveTokens {
			break
		}
		running += counts[i]
		cut = i
	}

	// Keep at least the most recent exchanges no matter how small the budget.
	if floor := len(history) - minPreservedMessages; cut > floor {
		cut = floor
	}
	if cut < 0 {
		cut = 0
	}

	// Starting mid-turn would hand the model an assistant reply with no
	// question in front of it, so move the cut forward to the next turn start.
	for cut < len(history)-1 && history[cut].Role != agentapi.RoleUser {
		cut++
	}
	return cut
}

// pruneToBudget keeps the newest suffix of msgs that fits budget, starting at a
// turn boundary. It is the deterministic fallback when summarization is
// unavailable.
func pruneToBudget(msgs []agentapi.Message, counts []int, budget int) []agentapi.Message {
	if len(msgs) == 0 {
		return msgs
	}

	cut, running := len(msgs), 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if running+counts[i] > budget {
			break
		}
		running += counts[i]
		cut = i
	}

	// Move forward to the next turn start; this only discards more, so the
	// budget still holds.
	for cut < len(msgs)-1 && msgs[cut].Role != agentapi.RoleUser {
		cut++
	}
	if cut >= len(msgs) {
		cut = len(msgs) - 1
	}
	return msgs[cut:]
}

// countEach returns the token count of every message, plus the per-message
// counts for reuse by the pruning helpers.
func (b *AgentBiz) countEach(msgs []agentapi.Message) ([]int, error) {
	counts := make([]int, len(msgs))
	for i, m := range msgs {
		n, err := b.countMessages([]agentapi.Message{m})
		if err != nil {
			return nil, err
		}
		counts[i] = n
	}
	return counts, nil
}

// countMessages counts through the injected counter, or through the real
// tokenizer when none was injected. Injection exists so tests can pin exact
// budgets rather than reason about BPE counts.
func (b *AgentBiz) countMessages(msgs []agentapi.Message) (int, error) {
	if b.deps.CountTokens != nil {
		return b.deps.CountTokens(msgs)
	}
	return tokenizer.CountMessages(msgs)
}

func sum(counts []int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}
