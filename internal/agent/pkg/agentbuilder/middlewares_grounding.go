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
// The original repo for this file is https://github.com/onexstack/onex.

package agentbuilder

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/pkg/modeljson"
)

// groundingNoticePrefix opens the line appended to an answer that did not hold
// up against the material it was built from.
const groundingNoticePrefix = "\n\n> ⚠️ "

// groundingTimeout bounds one check. Same budget as the security review and for
// the same reason: it runs after the answer exists, so nothing is waiting on
// its first token.
const groundingTimeout = 30 * time.Second

// groundingMaxEvidenceChars bounds what the reviewer is shown of the retrieved
// passages.
//
// The check is "did the answer contradict or invent material", which is a
// question about gist: a reviewer given every passage would spend its budget
// re-reading, and one given a truncated set is being asked the same question
// about the same material. The truncation is head-only rather than head-and-tail
// because the passages arrive best-first and the answer is most likely to be
// built from the top.
const groundingMaxEvidenceChars = 12000

// GroundingReviewer builds the middleware that checks a finished answer against
// the material the run retrieved. An interface, like SecurityReviewer, only so
// the builder can be handed a nil one.
type GroundingReviewer interface {
	Middleware(ctx context.Context) (adk.ChatModelAgentMiddleware, error)
}

// groundingReview reports whether an answer stands up against its evidence.
//
// # Why this exists when the prompt already says what to do
//
// The prompt tells the model which source wins where, and to say so when they
// disagree. Nothing checks that it did. That gap is not academic: the failure
// this whole path is prone to is a model that answers from its own memory while
// the log shows the corpus was queried, and that failure is *invisible* — the
// answer reads exactly like a grounded one, and the sources panel lists what the
// retrieval read, not what the answer used.
//
// So this is the layer that makes the instruction checkable rather than merely
// stated. It is the only place in the pipeline where anything looks at the
// answer and the evidence together.
//
// # Why it annotates instead of rewriting
//
// A rewrite would be a second answer the reader never asked for, produced with
// less context than the first (this call sees the answer and the passages, not
// the conversation), and it would make the answer change after the stream has
// finished. An annotation says what a reader needs to know — that this part did
// not hold up — and leaves the judgement where it belongs.
type groundingReview struct {
	reviewer model.BaseChatModel
	// evidenceTool names the tool whose output counts as the material to check
	// against. Empty means every tool's output does, which is the right default
	// for a deployment whose only evidence-producing tool is the knowledge base.
	evidenceTool string
}

// NewGroundingReview returns a GroundingReviewer backed by the given model, or
// nil when no model was supplied (which disables the feature).
func NewGroundingReview(reviewer model.BaseChatModel, evidenceTool string) GroundingReviewer {
	if reviewer == nil {
		return nil
	}
	return &groundingReview{reviewer: reviewer, evidenceTool: evidenceTool}
}

// Middleware returns the handler.
func (g *groundingReview) Middleware(context.Context) (adk.ChatModelAgentMiddleware, error) {
	return &groundingMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		reviewer:                     g.reviewer,
		evidenceTool:                 g.evidenceTool,
	}, nil
}

// groundingMiddleware annotates the final assistant message in place.
type groundingMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	reviewer     model.BaseChatModel
	evidenceTool string
}

// AfterAgent appends a caveat when the answer does not hold up against what was
// retrieved.
//
// It runs on the last assistant message with no tool calls — the answer, as
// opposed to the reasoning steps around it — and it leaves everything else in
// the state untouched.
//
// A run that retrieved nothing is skipped rather than reported: an answer built
// with no material is not an ungrounded answer, it is an answer to a different
// question, and telling the reader about it would be noise about a decision the
// prompt already governs.
func (m *groundingMiddleware) AfterAgent(ctx context.Context, state *adk.ChatModelAgentState) (context.Context, error) {
	if state == nil {
		return ctx, nil
	}
	idx := lastAnswerIndex(state.Messages)
	if idx < 0 {
		return ctx, nil
	}
	evidence := collectEvidence(state.Messages, m.evidenceTool)
	if evidence == "" {
		return ctx, nil
	}

	note, flagged := m.review(ctx, state.Messages[idx].Content, evidence)
	if !flagged {
		return ctx, nil
	}
	state.Messages[idx].Content += groundingNoticePrefix + note
	return ctx, nil
}

// collectEvidence concatenates what the run's evidence-producing tool returned.
//
// The tool messages are read in order and joined with a separator, because they
// are one body of material as far as the check is concerned — the question is
// "does the answer agree with what was read", and which call read which part is
// the retrieval's business, not this layer's.
func collectEvidence(messages []*schema.Message, toolName string) string {
	var sb strings.Builder
	for _, message := range messages {
		if message == nil || message.Role != schema.Tool || message.Content == "" {
			continue
		}
		if toolName != "" && message.ToolName != toolName {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString(message.Content)
		if sb.Len() >= groundingMaxEvidenceChars {
			break
		}
	}
	out := sb.String()
	if len([]rune(out)) > groundingMaxEvidenceChars {
		out = string([]rune(out)[:groundingMaxEvidenceChars])
	}
	return out
}

// groundingResponse is the reviewer's structured verdict.
type groundingResponse struct {
	// Flagged reports that the answer did not hold up.
	Flagged bool `json:"flagged"`
	// Note is the sentence shown to the reader. Only read when Flagged is true.
	Note string `json:"note"`
}

// groundingPrompt asks for the one judgement nothing else in the pipeline makes:
// does this answer agree with the material it was built from.
//
// The "do not flag" list carries as much weight as the rest. A reviewer that
// flags every summary — because a summary is by definition not a quotation —
// trains readers to ignore the note, which costs more than the check is worth.
const groundingPrompt = `你在核对一份 AI 助手的回答是否站得住。

下面【材料】是助手在回答前真正检索到的内容；【回答】是它给用户的最终答复。

只检查下面两类问题（其余一律不报）：
1. 回答把某个说法说成"课程里/资料里有"，但材料并不支持它——即编造了出处或内容。
2. 回答与材料明显矛盾，却**没有**说明这种不一致（材料里是 X，回答直接说成 Y，且没提两者不同）。

不要报（这些不是问题）：
- 回答简要概括了材料：概括本来就不是逐字复制。
- 回答用材料之外的通识做了补充，并且说明了这不是课程内容。
- 回答明确说明了材料与它自己的判断不一致——那正是期望的行为。
- 材料里没有相关片段（此时助手按通识回答是正确的）。

【材料】
%s

【回答】
%s

严格返回 JSON，不要有任何其他内容：
- 没有问题：{"flagged": false, "note": ""}
- 有问题：{"flagged": true, "note": "<一句中文说明，向读者交代哪一处说法在材料里找不到依据或与材料不一致；不要复述整段材料>"}`

// review runs the reviewer model. It reports the note and whether the answer was
// flagged; every failure returns "not flagged", because a check that cannot run
// must not change the answer.
func (m *groundingMiddleware) review(ctx context.Context, answer, evidence string) (string, bool) {
	if answer == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, groundingTimeout)
	defer cancel()

	resp, err := m.reviewer.Generate(ctx, []*schema.Message{
		schema.UserMessage(fmt.Sprintf(groundingPrompt, evidence, answer)),
	})
	if err != nil || resp == nil {
		slog.WarnContext(ctx, "[grounding] reviewer failed; the answer is left as written", "error", err)
		return "", false
	}

	var verdict groundingResponse
	if err := json.Unmarshal([]byte(modeljson.StripFence(resp.Content)), &verdict); err != nil {
		slog.WarnContext(ctx, "[grounding] unparsable verdict; the answer is left as written", "error", err)
		return "", false
	}
	if !verdict.Flagged {
		return "", false
	}
	slog.InfoContext(ctx, "[grounding] the answer did not hold up against the retrieved material", "note", verdict.Note)
	note := strings.TrimSpace(verdict.Note)
	if note == "" {
		// Flagged with nothing to say. A bare warning glyph inside a blockquote
		// reads as a rendering bug, so this degrades to the one sentence that is
		// true of every flagged answer.
		note = "以上回答中有一部分说法未能在检索到的资料中找到依据，请以课程页面为准。"
	}
	return note, true
}
