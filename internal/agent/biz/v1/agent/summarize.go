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
	"regexp"
	"slices"
	"strings"

	"github.com/cloudwego/eino/adk"
	adksummarization "github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/agent/pkg/chatmodel"
	"github.com/onexstack/onexai/internal/agent/pkg/tokenizer"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

const (
	// summarizerSystemPrompt frames the summarization call. It is deliberately
	// narrow: a summary that invents a conclusion is worse than no summary,
	// because later turns will treat it as established fact.
	summarizerSystemPrompt = "You are a conversation-archival assistant. Condense dialogue history " +
		"faithfully, preserving conclusions reached, the exact commands and tool calls that succeeded, " +
		"key findings, and any unresolved errors. Never invent details that are not in the transcript."

	// summarizationInstruction is the task given to the summarization model, and
	// is shared by the per-run middleware and the cross-turn compactor so both
	// condense the same way.
	summarizationInstruction = "请将以下历史对话记录压缩成一段简明扼要的总结。\n" +
		"必须保留：已经得出的结论、执行成功的关键命令与工具调用、关键发现、以及尚未解决的错误信息。\n" +
		"不要编造未出现过的内容。请用中文回答。"

	// summarizationRetries bounds retries inside the summarization middleware.
	summarizationRetries = 2

	// inRunTriggerRatio places the in-run summarizer's trigger above the
	// configured budget, so the two layers do not collide. The cross-turn
	// compactor reduces history to *just under* the budget; the run then adds
	// this turn's question and the tool schemas, which puts the first model call
	// of nearly every over-budget turn at or over the budget. A trigger set to
	// the budget itself therefore fires immediately, on every turn, redoing the
	// compactor's work at model-call cost. The headroom leaves the in-run layer
	// to do what only it can: rescue a run that a tool output has ballooned.
	inRunTriggerRatio = 1.5

	// preservedUserMessageRatio is the share of the budget reserved for user
	// messages kept verbatim through an in-run summarization. Reserving a share
	// that no realistic question can exhaust keeps this turn's question whole:
	// a message too big for the block is not kept as-is, it is cut down to fit,
	// and with the allowance sized from the budget that should never happen.
	//
	// This is applied by newUserMessageFinalize rather than by the library. eino
	// used to take the allowance as PreserveUserMessages.MaxTokens; v0.9.21
	// removed the field and sizes the block from a fixed 30k-token constant
	// instead, which for a deployment budget of 500k is a twelfth of the share
	// asked for here. 0.75 leaves a quarter of the trigger for the summary.
	preservedUserMessageRatio = 0.75

	// userMessagesOmittedNote marks a preserved block that does not list every
	// user message. The summarization prompt describes the block as holding ALL
	// user messages, so one quietly shortened for budget reads as a complete
	// record of what the user asked for.
	userMessagesOmittedNote = "（更早的用户消息因超出保留额度而省略，完整记录见摘要正文。）"
)

// allUserMessagesBlock matches the section the summarization prompt asks the
// model to emit, and that the finalizer rewrites with verbatim user messages.
//
// The pattern is the library's own, spelled out here because the finalize step
// is ours: since eino v0.9.21 a caller cannot say how many tokens of user
// messages the block may hold, so it is sized in this file and has to be found
// in the summary to be replaced.
var allUserMessagesBlock = regexp.MustCompile(`(?s)<all_user_messages>.*</all_user_messages>`)

// NewSummarizer builds the history condenser the agent biz uses to compact a
// session that has outgrown its budget. It returns nil when no chat model is
// configured, which disables summarization and leaves compaction to pruning.
func NewSummarizer(cm *chatmodel.OpenAIChatModel) func(context.Context, []agentapi.Message) (string, error) {
	if cm == nil {
		return nil
	}
	return func(ctx context.Context, msgs []agentapi.Message) (string, error) {
		// This call runs on the *request* context, so it would otherwise inherit
		// the ceiling the user's own surface was capped at — and a group chat's
		// 2048-token answer ceiling is not a statement about how long this
		// session's archive may be. Clearing it here is deliberate rather than
		// incidental: the summary is not an answer, so no answer's ceiling
		// applies to it, and the deployment's llm.max-tokens still does.
		ctx = chatmodel.WithMaxOutputTokens(ctx, 0)
		systemPrompt := summarizerSystemPrompt
		userPrompt := renderTranscript(msgs)
		// A caller can ask for a different task on the same model (memory
		// extraction is the first such caller). The request travels on the
		// context rather than as a message so history compaction, whose input
		// may itself contain system entries, cannot be mistaken for one.
		if override, ok := systemPromptFromContext(ctx); ok {
			systemPrompt = override
			userPrompt = renderPlainTranscript(msgs)
		}
		resp, err := cm.Generate(ctx, []*schema.Message{
			schema.SystemMessage(systemPrompt),
			schema.UserMessage(userPrompt),
		})
		if err != nil {
			return "", err
		}
		if resp == nil {
			return "", fmt.Errorf("summarizer returned no message")
		}
		return resp.Content, nil
	}
}

// renderTranscript lays the messages out as a readable transcript. System
// entries are skipped, and image payloads are never inlined: they are data
// URLs whose base64 body would dwarf the text being summarized.
func renderTranscript(msgs []agentapi.Message) string {
	var sb strings.Builder
	sb.WriteString(summarizationInstruction)
	sb.WriteString("\n\n历史对话记录如下：\n")

	for _, m := range msgs {
		if m.Role == agentapi.RoleSystem {
			continue
		}
		if m.Content == "" && len(m.Images) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "[%s]: %s", m.Role, m.Content)
		if len(m.Images) > 0 {
			fmt.Fprintf(&sb, " （附带 %s）", describeImages(m.Images))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

type systemPromptContextKey struct{}

// withSystemPrompt asks the shared summarizer to run a different instruction
// against the same model.
func withSystemPrompt(ctx context.Context, prompt string) context.Context {
	return context.WithValue(ctx, systemPromptContextKey{}, prompt)
}

func systemPromptFromContext(ctx context.Context) (string, bool) {
	prompt, ok := ctx.Value(systemPromptContextKey{}).(string)
	return prompt, ok && strings.TrimSpace(prompt) != ""
}

// renderPlainTranscript is renderTranscript without the summarization framing.
// It is used when the caller supplied its own system prompt.
func renderPlainTranscript(msgs []agentapi.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role == agentapi.RoleSystem {
			continue
		}
		if m.Content == "" && len(m.Images) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "[%s]: %s", m.Role, m.Content)
		if len(m.Images) > 0 {
			fmt.Fprintf(&sb, " （附带 %s）", describeImages(m.Images))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// describeImages renders a count and, when they are known, the shapes.
//
// # Why anything more than a count
//
// A summary that says "one image" tells the model that a turn was not purely
// textual and nothing else. The type and the dimensions say whether it was a
// screenshot of an error, a diagram, or a photograph — which is what a later
// question about it is likely to be about, and what the summary should keep if
// it is going to be useful.
//
// Nothing here reads the bytes. The payload is a data URL when the turn was
// live, and inlining one would put a base64 body into the summarizer's prompt,
// dwarfing the text it was asked to condense — the same reason the history is
// stored as references.
func describeImages(refs []media.Ref) string {
	shapes := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		shape := r.MimeType
		if shape == "" {
			shape = "图片"
		}
		if r.Width > 0 && r.Height > 0 {
			shape = fmt.Sprintf("%s %dx%d", shape, r.Width, r.Height)
		}
		shapes[shape] = struct{}{}
	}

	// Sorted so the same history renders the same way twice: a summary that
	// changed its wording between runs would look like the conversation had.
	distinct := make([]string, 0, len(shapes))
	for shape := range shapes {
		distinct = append(distinct, shape)
	}
	slices.Sort(distinct)

	if len(distinct) == 1 {
		return fmt.Sprintf("%d 张图片（%s）", len(refs), distinct[0])
	}
	return fmt.Sprintf("%d 张图片（%s）", len(refs), strings.Join(distinct, "、"))
}

// summarizationLimits derives the in-run middleware's trigger and its verbatim
// user-message allowance from the cross-turn budget. It exists as its own
// function so the relationship — the trigger clears the budget the compactor
// reduces to, and the allowance is ours rather than the library's derived
// default — is pinned by a test instead of buried in a constructor.
func summarizationLimits(maxTokens int) (trigger, preservedUserTokens int) {
	return int(InRunCompactionTrigger(maxTokens)),
		int(float64(maxTokens) * preservedUserMessageRatio)
}

// InRunCompactionTrigger is the token count at which in-run context compaction
// begins: the in-run summarizer's trigger, and the tool-output reducer's clear
// threshold.
//
// The two share it deliberately. The reducer is registered ahead of the
// summarizer, so at that point it first drops the oldest tool results — a
// local, model-free operation — and the summarizer only runs if that was not
// enough. Separate thresholds would let the expensive layer fire while the
// cheap one was still asleep.
func InRunCompactionTrigger(maxTokens int) int64 {
	if maxTokens <= 0 {
		return 0
	}
	return int64(float64(maxTokens) * inRunTriggerRatio)
}

// newSummarizationHandler builds the in-run summarization middleware, or nil
// when it cannot be configured.
//
// Its trigger sits above the configured budget rather than at it: see
// inRunTriggerRatio. The ceiling still has to clear the model's window, because
// this middleware does not chunk — it hands the whole context to the summary
// model in a single call, so triggering it beyond the window would make the
// rescue attempt fail exactly the way the run it is rescuing would have. That
// is why the option's documented limit is a fraction of the window, not the
// window itself.
func newSummarizationHandler(ctx context.Context, cm *chatmodel.OpenAIChatModel, maxTokens int) adk.ChatModelAgentMiddleware {
	if cm == nil || maxTokens <= 0 {
		return nil
	}

	trigger, preserved := summarizationLimits(maxTokens)

	retries := summarizationRetries
	mw, err := adksummarization.New(ctx, &adksummarization.Config{
		Model:   cm,
		Trigger: &adksummarization.TriggerCondition{ContextTokens: trigger},
		TokenCounter: func(_ context.Context, in *adksummarization.TokenCounterInput) (int, error) {
			return tokenizer.CountSchemaMessages(in.Messages, in.Tools)
		},
		UserInstruction: summarizationInstruction,
		Retry:           &adksummarization.RetryConfig{MaxRetries: &retries},
		Finalize:        newUserMessageFinalize(preserved),
	})
	if err != nil {
		// Losing the middleware is survivable — the truncation guard and the
		// cross-turn compactor remain — so warn rather than fail the engine.
		slog.WarnContext(ctx, "summarization middleware disabled, context is bounded by truncation alone",
			"error", err)
		return nil
	}
	return mw
}

// newUserMessageFinalize rebuilds the summary's user-message block against the
// allowance summarizationLimits derives.
//
// # Why this is ours rather than the library's
//
// eino v0.9.21 dropped PreserveUserMessages. The block is now always sized from
// a fixed 30k-token constant, whatever the run's context budget is, and the
// selection loop that fills it is unexported — so the only way to keep a larger
// allowance, which is the whole point of preservedUserMessageRatio, is to
// rewrite the block after the fact.
//
// Everything else remains the library's: the preamble, the system-message
// split, and the judgement of which messages the summarizer chose to retain all
// come out of DefaultFinalize untouched. Only the block is replaced.
func newUserMessageFinalize(preserved int) adksummarization.FinalizeFunc {
	return func(ctx context.Context, original []*schema.Message, summary *schema.Message) ([]*schema.Message, error) {
		out, err := adksummarization.DefaultFinalize(ctx, original, summary)
		if err != nil {
			return nil, err
		}

		// DefaultFinalize puts the rewritten summary last, after any system
		// messages it moved to the front. Scanning back from there keeps this
		// working if that layout ever gains a message after the summary.
		for i := len(out) - 1; i >= 0; i-- {
			message := out[i]
			if message == nil {
				continue
			}
			span := lastBlockSpan(allUserMessagesBlock, message.Content)
			if span == nil {
				continue
			}

			block, err := renderPreservedUserMessages(original, preserved)
			if err != nil {
				return nil, err
			}
			if block == "" {
				// Nothing to re-size — the run has no user messages, or the
				// allowance cannot hold even the newest one. Either way the
				// library's own block stands, which beats emptying it.
				return out, nil
			}

			// Copy rather than mutate: out is fresh, but the message in it may
			// still be the caller's.
			rewritten := ptr.Clone(message)
			rewritten.Content = message.Content[:span[0]] + block + message.Content[span[1]:]
			out[i] = rewritten

			return out, nil
		}

		// The summarizer did not emit the section, so there is no block to
		// re-size; DefaultFinalize has already had its say.
		return out, nil
	}
}

// renderPreservedUserMessages lays out the newest run of user messages that fits
// inside budget, wrapped in the tags the prompt and the summarizer agree on.
//
// Selection runs newest-first and stops at the first message that does not fit,
// so the block always holds a contiguous run of the most recent questions. The
// message that overruns is dropped rather than cut down to fit: eino truncates
// an oversized one to its first and last 1000 runes, which turns a pasted
// question into two excerpts with its middle — usually the detail being asked
// about — cut out. A run with nothing to show returns "" and leaves the
// library's block in place, so a question too large even for this allowance
// still reaches the model truncated rather than not at all.
func renderPreservedUserMessages(msgs []*schema.Message, budget int) (string, error) {
	type candidate struct {
		text   string
		tokens int
	}

	candidates := make([]candidate, 0, len(msgs))
	for _, m := range msgs {
		if m == nil || m.Role != schema.User {
			continue
		}
		text := userMessageText(m)
		if text == "" {
			continue
		}
		// Counted the same way the middleware counts the context it is
		// rescuing, so the allowance is spent in the currency it was set in.
		tokens, err := tokenizer.CountSchemaMessages([]*schema.Message{m}, nil)
		if err != nil {
			return "", err
		}
		candidates = append(candidates, candidate{text: text, tokens: tokens})
	}
	if len(candidates) == 0 {
		return "", nil
	}

	spent, kept := 0, 0
	for i := len(candidates) - 1; i >= 0; i-- {
		if spent+candidates[i].tokens > budget {
			break
		}
		spent += candidates[i].tokens
		kept++
	}
	if kept == 0 {
		return "", nil
	}
	selected := candidates[len(candidates)-kept:]

	var sb strings.Builder
	sb.WriteString("<all_user_messages>\n")
	if kept < len(candidates) {
		sb.WriteString(userMessagesOmittedNote)
		sb.WriteString("\n")
	}
	for i, c := range selected {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("    - ")
		sb.WriteString(c.text)
	}
	sb.WriteString("\n</all_user_messages>")

	return sb.String(), nil
}

// userMessageText is what a user message contributes to the preserved block. It
// resolves text parts before the plain content field, as the library does: a
// multi-part message built for a vision model carries its text in the parts and
// leaves Content empty.
func userMessageText(m *schema.Message) string {
	var parts []string
	for _, part := range m.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return m.Content
}

// lastBlockSpan returns the span of the last match of re in s, or nil when
// there is none. The summarizer can echo the section more than once, and the
// one the finalizer rewrote is the last.
func lastBlockSpan(re *regexp.Regexp, s string) []int {
	matches := re.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return nil
	}
	return matches[len(matches)-1]
}
