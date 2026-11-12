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
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// exchanges builds n user/assistant pairs, the exact shape the agent persists
// (agent.go saves only those two roles).
func exchanges(n int) []agentapi.Message {
	msgs := make([]agentapi.Message, 0, n*2)
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			agentapi.Message{Role: agentapi.RoleUser, Content: fmt.Sprintf("q%d", i)},
			agentapi.Message{Role: agentapi.RoleAssistant, Content: fmt.Sprintf("a%d", i)},
		)
	}
	return msgs
}

// flatCounter charges per tokens for every message, so a test can pin the exact
// budget boundary instead of reasoning about BPE counts.
func flatCounter(per int) func([]agentapi.Message) (int, error) {
	return func(msgs []agentapi.Message) (int, error) { return len(msgs) * per, nil }
}

// lengthCounter charges one token per character, so a single oversized message
// can dominate the budget the way a pasted document or a tool result does.
func lengthCounter(msgs []agentapi.Message) (int, error) {
	total := 0
	for _, m := range msgs {
		total += len([]rune(m.Content))
	}
	return total, nil
}

// recordingSummarizer records every summarization call and returns a summary
// that identifies the call, so tests can assert how a history was split. A
// non-nil err makes it behave as a failing summarizer.
type recordingSummarizer struct {
	mu    sync.Mutex
	calls [][]agentapi.Message
	err   error
}

func (s *recordingSummarizer) summarize(_ context.Context, msgs []agentapi.Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, append([]agentapi.Message(nil), msgs...))
	if s.err != nil {
		return "", s.err
	}
	return fmt.Sprintf("summary-%d", len(s.calls)), nil
}

func (s *recordingSummarizer) recorded() [][]agentapi.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]agentapi.Message(nil), s.calls...)
}

// compactionBizWith builds the AgentBiz fields the compaction path reads, over
// a fresh in-memory session store that is returned for assertions.
func compactionBizWith(
	count func([]agentapi.Message) (int, error),
	summarize func(context.Context, []agentapi.Message) (string, error),
	budget, preserve int,
) (*AgentBiz, mem.SessionStore) {
	store := mem.NewSessionStore(nil, "test", 0)
	return New(Deps{
		Sessions:              store,
		MaxContextTokens:      budget,
		ContextPreserveTokens: preserve,
		Summarize:             summarize,
		CountTokens:           count,
	}), store
}

// compactionBiz is compactionBizWith over the flat per-message counter.
func compactionBiz(
	summarize func(context.Context, []agentapi.Message) (string, error), budget, preserve, per int,
) (*AgentBiz, mem.SessionStore) {
	return compactionBizWith(flatCounter(per), summarize, budget, preserve)
}

// TestAskHandsTheEngineACompactedHistory is the end-to-end check of the wiring
// inside this package: a session that has outgrown the budget must reach the
// engine already condensed, not be compacted and then ignored.
func TestAskHandsTheEngineACompactedHistory(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSummarizer{}
	engine := &captureEngine{opts: make(chan agentapi.RunOptions, 1)}
	store := mem.NewSessionStore(nil, "test", 0)
	biz := New(Deps{
		Engine:               engine,
		Sessions:             store,
		Stops:                runstop.NewLocal(),
		MaxConcurrentPerConv: 1,
		MaxContextTokens:     10,
		Summarize:            rec.summarize,
		CountTokens:          flatCounter(1),
	})

	history := exchanges(10) // 20 messages against a budget of 10
	if err := store.Save(ctx, "c1", history); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	ch, err := biz.Ask(ctx, &agentapi.AskRequest{SessionID: "c1", Input: agentapi.Input{Text: "hi"}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	drain(t, ch)

	// What the engine saw: the compacted history, then this turn's question.
	seen := engine.msgs
	if len(seen) != 6 {
		t.Fatalf("engine was handed %d messages, want 5 compacted plus the question", len(seen))
	}
	if !strings.HasPrefix(seen[0].Content, summaryMarker) {
		t.Fatalf("engine saw an uncompacted history starting with %q", seen[0].Content)
	}
	if !reflect.DeepEqual(seen[1:5], history[16:]) {
		t.Fatal("engine did not receive the preserved tail verbatim")
	}
	if seen[5].Role != agentapi.RoleUser || seen[5].Content != "hi" {
		t.Fatalf("engine did not receive the question last: %q", seen[5].Content)
	}

	// The compaction was persisted, and this turn appended to it exactly once
	// (not appended to the uncompacted history, and not duplicated).
	stored, err := store.Load(ctx, "c1")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	// 5 compacted + question + answer + the run's tool record. The record is
	// written after the answer rather than folded into it, so it adds a message
	// rather than lengthening one; see the note in persist.
	if len(stored) != 8 {
		t.Fatalf("session holds %d messages, want 5 compacted + question + answer + tool record", len(stored))
	}
	if !strings.HasPrefix(stored[0].Content, summaryMarker) {
		t.Fatalf("the compacted prefix was lost: %q", stored[0].Content)
	}
	if stored[5].Content != "hi" || stored[6].Content != "ok" {
		t.Fatalf("this turn was not persisted in order: %q, %q", stored[5].Content, stored[6].Content)
	}
	// The record lands after the answer it describes, and the next turn is
	// where it does its work: it must not be visible to the run that produced
	// it, which already knew what it called.
	if stored[7].Role != agentapi.RoleUser || !strings.HasPrefix(stored[7].Content, toolTrailMarker) {
		t.Fatalf("the tool record was not persisted last: %+v", stored[7])
	}
	// And the engine this turn was handed must not have seen it, or the
	// question would no longer be the last thing a model reads.
	if len(seen) != 6 || seen[5].Content != "hi" {
		t.Fatalf("the engine saw the tool record it should not have: %+v", seen)
	}
}

// TestShrinkHistory_DoesNotSummarizeAnEmptyStretch is the regression test for a
// defect an end-to-end run against a live model surfaced: with a history no
// longer than the preserve floor, every message landed in the preserved window,
// so there was nothing to condense — and the code asked the model to summarize
// an empty transcript anyway. The model answered with a plausible-looking
// summary of nothing, which was prepended and persisted, growing the history on
// every turn and derailing the conversation that followed.
func TestShrinkHistory_DoesNotSummarizeAnEmptyStretch(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSummarizer{}
	// Four messages are exactly the preserve floor, so the cut lands at zero.
	// The long question is what pushes the total over the budget, not the
	// history: compacting history cannot fix that, so it must not try.
	biz, store := compactionBizWith(lengthCounter, rec.summarize, 20, 0)
	history := exchanges(2) // 4 messages, 8 tokens
	if err := store.Save(ctx, "s", history); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	got := biz.shrinkHistory(ctx, "s", history, agentapi.Message{Role: agentapi.RoleUser, Content: "a much longer question"})

	if n := len(rec.recorded()); n != 0 {
		t.Fatalf("summarizer was called %d times with nothing to summarize", n)
	}
	if !reflect.DeepEqual(got, history) {
		t.Fatalf("history was rewritten with nothing to condense: %d messages", len(got))
	}
	stored, err := store.Load(ctx, "s")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if !reflect.DeepEqual(stored, history) {
		t.Fatal("a history with nothing to condense was persisted")
	}
}

// TestShrinkHistory_TrimsAPreservedWindowOverBudget covers the other half of
// the same branch: when the preserved window is itself larger than the budget,
// the floor has to yield, otherwise the over-window request still goes out.
func TestShrinkHistory_TrimsAPreservedWindowOverBudget(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSummarizer{}
	// Four messages of 100k tokens each: the preserve budget (a third of 200k)
	// cannot even hold one, so the cut lands at zero and there is nothing to
	// summarize — but the history is still twice the budget.
	const per = 100000
	biz, store := compactionBiz(rec.summarize, 200000, 0, per)
	history := exchanges(2) // 4 messages
	if err := store.Save(ctx, "s", history); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	got := biz.shrinkHistory(ctx, "s", history, agentapi.Message{Role: agentapi.RoleUser, Content: "new"})

	if n := len(rec.recorded()); n != 0 {
		t.Fatalf("summarizer was called %d times with nothing to summarize", n)
	}
	if !reflect.DeepEqual(got, history[2:]) {
		t.Fatalf("kept %d messages, want the newest 2 that fit the budget", len(got))
	}
	tokens, err := biz.countMessages(got)
	if err != nil {
		t.Fatalf("count pruned history: %v", err)
	}
	if tokens > 200000 {
		t.Fatalf("trimmed history is %d tokens, still over the budget", tokens)
	}

	stored, err := store.Load(ctx, "s")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if !reflect.DeepEqual(stored, got) {
		t.Fatal("the trimmed history was not persisted")
	}
}

func TestShrinkHistory_SkipsWhenUnderBudget(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSummarizer{}
	biz, store := compactionBiz(rec.summarize, 100, 0, 1)
	history := exchanges(5) // 10 messages, plus the incoming one = 11 tokens

	if err := store.Save(ctx, "s", history); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	got := biz.shrinkHistory(ctx, "s", history, agentapi.Message{Role: agentapi.RoleUser, Content: "new"})

	if n := len(rec.recorded()); n != 0 {
		t.Fatalf("summarizer called %d times under budget, want 0", n)
	}
	if !reflect.DeepEqual(got, history) {
		t.Fatalf("history changed under budget: got %d messages, want %d", len(got), len(history))
	}
	stored, err := store.Load(ctx, "s")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if !reflect.DeepEqual(stored, history) {
		t.Fatalf("store was rewritten under budget: got %d messages, want %d", len(stored), len(history))
	}
}

func TestShrinkHistory_SummarizesOldestAndKeepsNewest(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSummarizer{}
	// 20 messages against a budget of 10: the oldest are condensed and the
	// newest minPreservedMessages survive verbatim.
	biz, store := compactionBiz(rec.summarize, 10, 0, 1)
	history := exchanges(10)

	got := biz.shrinkHistory(ctx, "s", history, agentapi.Message{Role: agentapi.RoleUser, Content: "new"})

	calls := rec.recorded()
	if len(calls) != 1 {
		t.Fatalf("summarizer called %d times, want 1", len(calls))
	}
	if !reflect.DeepEqual(calls[0], history[:16]) {
		t.Fatalf("summarizer saw %d messages, want the 16 oldest", len(calls[0]))
	}

	if len(got) != 5 {
		t.Fatalf("compacted to %d messages, want one summary plus 4 preserved", len(got))
	}
	// The summary is a user message on purpose: toSchemaMessages drops
	// system-role history, so a system-role summary would never reach the model.
	if got[0].Role != agentapi.RoleUser {
		t.Fatalf("summary role is %q, want %q", got[0].Role, agentapi.RoleUser)
	}
	if !strings.HasPrefix(got[0].Content, summaryMarker) || !strings.Contains(got[0].Content, "summary-1") {
		t.Fatalf("first message is not the marked summary: %q", got[0].Content)
	}
	if !reflect.DeepEqual(got[1:], history[16:]) {
		t.Fatal("the newest messages were not preserved verbatim")
	}

	stored, err := store.Load(ctx, "s")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if !reflect.DeepEqual(stored, got) {
		t.Fatal("compacted history was not persisted")
	}
}

func TestShrinkHistory_HonoursContextPreserveTokens(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSummarizer{}
	// Explicit preserve budget of 8 tokens = 8 messages, well above the
	// minPreservedMessages floor, so it is the configured value that binds.
	biz, _ := compactionBiz(rec.summarize, 20, 8, 1)
	history := exchanges(20) // 40 messages

	got := biz.shrinkHistory(ctx, "s", history, agentapi.Message{Role: agentapi.RoleUser, Content: "new"})

	calls := rec.recorded()
	if len(calls) != 1 {
		t.Fatalf("summarizer called %d times, want 1", len(calls))
	}
	if !reflect.DeepEqual(calls[0], history[:32]) {
		t.Fatalf("summarizer saw %d messages, want the 32 oldest", len(calls[0]))
	}
	if len(got) != 9 {
		t.Fatalf("compacted to %d messages, want one summary plus 8 preserved", len(got))
	}
	if !reflect.DeepEqual(got[1:], history[32:]) {
		t.Fatal("the preserved window does not match context-preserve-tokens")
	}
}

func TestShrinkHistory_ConvergesOnOverWindowHistory(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSummarizer{}

	// 14 messages at 100k tokens each is 1.4M — the size that produced the
	// production 400 (1412554 tokens) — against a 1M budget. Feeding that to a
	// single summarization call would fail exactly the way the run it is
	// rescuing did, so the chunked path must split it.
	const per = 100000
	budget := 1000000
	biz, store := compactionBiz(rec.summarize, budget, 0, per)
	history := exchanges(7) // 14 messages

	got := biz.shrinkHistory(ctx, "s", history, agentapi.Message{Role: agentapi.RoleUser, Content: "new"})

	// preserve defaults to budget/3 = 333k, so the cut keeps 4 messages and
	// hands the 10 oldest (1M tokens) to the summarizer.
	calls := rec.recorded()
	if len(calls) != 5 {
		t.Fatalf("summarizer called %d times, want 5 chunks of 2 messages", len(calls))
	}
	seen := 0
	for i, c := range calls {
		// summaryChunkTokens (200k) at 100k per message = 2 messages per chunk.
		if len(c) > 2 {
			t.Fatalf("chunk %d carried %d messages, exceeding the 200k-token chunk limit", i, len(c))
		}
		seen += len(c)
	}
	if seen != 10 {
		t.Fatalf("chunks covered %d messages, want the 10 oldest", seen)
	}

	// The compacted history must now fit the budget it was measured against.
	if len(got) != 5 {
		t.Fatalf("compacted to %d messages, want one folded summary plus 4 preserved", len(got))
	}
	if !strings.Contains(got[0].Content, "summary-1") || !strings.Contains(got[0].Content, "summary-5") {
		t.Fatalf("the folded summary does not carry every chunk: %q", got[0].Content)
	}
	tokens, err := biz.countMessages(got)
	if err != nil {
		t.Fatalf("count compacted history: %v", err)
	}
	if tokens > budget {
		t.Fatalf("compacted history is %d tokens, still over the %d budget", tokens, budget)
	}

	stored, err := store.Load(ctx, "s")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if !reflect.DeepEqual(stored, got) {
		t.Fatal("compacted history was not persisted")
	}
}

func TestShrinkHistory_FallsBackToPruneOnSummarizeError(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSummarizer{err: errors.New("boom")}
	biz, store := compactionBiz(rec.summarize, 10, 0, 1)
	history := exchanges(10) // 20 messages

	got := biz.shrinkHistory(ctx, "s", history, agentapi.Message{Role: agentapi.RoleUser, Content: "new"})

	if len(rec.recorded()) == 0 {
		t.Fatal("summarizer was never attempted")
	}
	// The turn still has to go out: the newest history that fits survives, cut
	// at a user message so no assistant reply is orphaned.
	if !reflect.DeepEqual(got, history[10:]) {
		t.Fatalf("fallback kept %d messages, want the newest 10", len(got))
	}
	if got[0].Role != agentapi.RoleUser {
		t.Fatalf("fallback left the history starting with %q, want a user message", got[0].Role)
	}
	for _, m := range got {
		if strings.HasPrefix(m.Content, summaryMarker) {
			t.Fatal("a summary marker survived a failed summarization")
		}
	}

	stored, err := store.Load(ctx, "s")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if !reflect.DeepEqual(stored, got) {
		t.Fatal("pruned history was not persisted")
	}
}

func TestShrinkHistory_NilSummarizerPrunes(t *testing.T) {
	ctx := context.Background()
	// Without a summarizer the budget still has to be enforced, otherwise a
	// deployment that never wired one would keep hitting the window.
	biz, _ := compactionBiz(nil, 10, 0, 1)
	history := exchanges(10)

	got := biz.shrinkHistory(ctx, "s", history, agentapi.Message{Role: agentapi.RoleUser, Content: "new"})

	if !reflect.DeepEqual(got, history[10:]) {
		t.Fatalf("kept %d messages, want the newest 10", len(got))
	}
}

func TestShrinkHistory_ZeroBudgetIsANoOp(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSummarizer{}
	// MaxContextTokens is the only opt-out; the history must come back untouched.
	biz, store := compactionBiz(rec.summarize, 0, 0, 1)
	history := exchanges(30)

	got := biz.shrinkHistory(ctx, "s", history, agentapi.Message{Role: agentapi.RoleUser, Content: "new"})

	if !reflect.DeepEqual(got, history) {
		t.Fatal("a zero budget still rewrote the history")
	}
	if n := len(rec.recorded()); n != 0 {
		t.Fatalf("summarizer called %d times with context management disabled", n)
	}
	stored, err := store.Load(ctx, "s")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("store was written with context management disabled: %d messages", len(stored))
	}
}

func TestShrinkHistory_EmptyHistoryIsANoOp(t *testing.T) {
	ctx := context.Background()
	rec := &recordingSummarizer{}
	biz, _ := compactionBiz(rec.summarize, 10, 0, 1)

	got := biz.shrinkHistory(ctx, "s", nil, agentapi.Message{Role: agentapi.RoleUser, Content: "new"})

	if len(got) != 0 {
		t.Fatalf("got %d messages from an empty history, want 0", len(got))
	}
	if n := len(rec.recorded()); n != 0 {
		t.Fatalf("summarizer called %d times for an empty history", n)
	}
}

// countsOf returns a per-message token count of 1, matching flatCounter(1).
func countsOf(msgs []agentapi.Message) []int {
	counts := make([]int, len(msgs))
	for i := range counts {
		counts[i] = 1
	}
	return counts
}

func TestPruneToBudget_CutsAtUserBoundary(t *testing.T) {
	msgs := exchanges(10) // 20 messages, alternating user/assistant
	// A budget of 9 puts the raw cut on index 11, an assistant message; the cut
	// must advance to the next user message so no reply is orphaned.
	got := pruneToBudget(msgs, countsOf(msgs), 9)

	if len(got) == 0 {
		t.Fatal("prune returned nothing")
	}
	if got[0].Role != agentapi.RoleUser {
		t.Fatalf("pruned history starts with %q, want a user message", got[0].Role)
	}
	if !reflect.DeepEqual(got, msgs[12:]) {
		t.Fatalf("pruned to %d messages, want the newest 8", len(got))
	}
}

func TestPruneToBudget_KeepsNewestWhenNothingFits(t *testing.T) {
	msgs := exchanges(2)[:3] // user, assistant, user
	counts := []int{100, 100, 100}

	got := pruneToBudget(msgs, counts, 50)

	// One message can exceed the whole budget. Returning nothing would leave the
	// model with no conversation at all, so the newest message survives.
	if len(got) != 1 {
		t.Fatalf("pruned to %d messages, want 1", len(got))
	}
	if got[0].Content != msgs[2].Content {
		t.Fatalf("kept %q, want the newest message %q", got[0].Content, msgs[2].Content)
	}
}

func TestPruneToBudget_ResultFitsBudget(t *testing.T) {
	msgs := exchanges(20)
	counts := make([]int, len(msgs))
	for i := range counts {
		counts[i] = 7
	}
	const budget = 50

	got := pruneToBudget(msgs, counts, budget)

	if len(got) == 0 {
		t.Fatal("prune returned nothing")
	}
	total := 0
	for range got {
		total += 7
	}
	if total > budget {
		t.Fatalf("pruned history is %d tokens, over the %d budget", total, budget)
	}
	if got[0].Role != agentapi.RoleUser {
		t.Fatalf("pruned history starts with %q, want a user message", got[0].Role)
	}
}

func TestPruneToBudget_EmptyHistory(t *testing.T) {
	if got := pruneToBudget(nil, nil, 100); len(got) != 0 {
		t.Fatalf("got %d messages from an empty history, want 0", len(got))
	}
}

func TestCutForPreserve_NeverTrimsBelowTheFloor(t *testing.T) {
	msgs := exchanges(5) // 10 messages
	// A budget far below one message still has to leave the most recent
	// exchange intact.
	cut := cutForPreserve(msgs, countsOf(msgs), 0)

	if cut > len(msgs)-minPreservedMessages {
		t.Fatalf("cut at %d trims below the %d-message floor", cut, minPreservedMessages)
	}
	if msgs[cut].Role != agentapi.RoleUser {
		t.Fatalf("cut at %d is not a user boundary (%q)", cut, msgs[cut].Role)
	}
}
