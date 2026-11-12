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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// truncatedHeadRatio splits a truncated tool output between its head and its
// tail. The tail is favoured because failures, counts and conclusions usually
// land at the end of a tool's output.
const truncatedHeadRatio = 0.3

// contextGuardHandler caps the size of a single tool output before each model
// call.
//
// It exists because history compaction only bounds what a run *starts* with: a
// single MCP tool can return hundreds of thousands of tokens in one message,
// pushing the running context past the model's window mid-run. The
// summarization middleware cannot rescue that, because its own summarization
// call would be handed the oversized context and fail the same way. Truncation
// is local, needs no model call, and cannot fail.
//
// It only ever truncates; it never drops a message. Removing a tool result
// would orphan the tool call it answers, which the provider rejects outright.
type contextGuardHandler struct {
	*adk.BaseChatModelAgentMiddleware
	// maxRunes is the cap on one tool message, in characters. Zero disables it.
	maxRunes int
}

func newContextGuardHandler(maxChars int) *contextGuardHandler {
	return &contextGuardHandler{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		maxRunes:                     maxChars,
	}
}

// BeforeModelRewriteState truncates any oversized tool output in place.
//
// The trimmed message is at or under the cap by construction, so it is
// idempotent without having to remember what it already touched: this hook runs
// before every model call of a run, and a message that kept growing a fresh
// notice on each pass would be shredded rather than trimmed.
func (h *contextGuardHandler) BeforeModelRewriteState(
	ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if h.maxRunes <= 0 || state == nil {
		return ctx, state, nil
	}

	for _, message := range state.Messages {
		if message == nil || message.Role != schema.Tool || message.Content == "" {
			continue
		}
		runes := []rune(message.Content)
		if len(runes) <= h.maxRunes {
			continue
		}

		// The notice is spliced into the same budget, and the count it reports
		// feeds its own length, so reserve against the largest count this
		// message can produce and let the leftover absorb the slack.
		budget := h.maxRunes - len([]rune(truncationNotice(len(runes))))
		if budget <= 0 {
			// A cap too small to hold the notice itself. Enforce the cap without
			// a marker rather than re-truncating a notice on every pass.
			message.Content = string(runes[len(runes)-h.maxRunes:])
			slog.WarnContext(ctx, "tool output cap is too small to hold the truncation notice; content was elided without a marker",
				"tool_name", message.ToolName, "tool_call_id", message.ToolCallID,
				"original_runes", len(runes), "max_runes", h.maxRunes)
			continue
		}

		omitted := len(runes) - budget
		head := int(float64(budget) * truncatedHeadRatio)
		tail := budget - head
		message.Content = string(runes[:head]) + truncationNotice(omitted) + string(runes[len(runes)-tail:])

		slog.WarnContext(ctx, "tool output truncated to keep the run within the context budget",
			"tool_name", message.ToolName,
			"tool_call_id", message.ToolCallID,
			"original_runes", len(runes),
			"max_runes", h.maxRunes,
			"omitted_runes", omitted,
		)
	}
	return ctx, state, nil
}

// truncationNotice is the marker spliced into a trimmed tool output. It tells
// the model that what it is reading is incomplete, so it can re-run the tool
// with narrower arguments instead of trusting a hole in the middle.
func truncationNotice(omitted int) string {
	return fmt.Sprintf(
		"\n\n... [OUTPUT TRUNCATED: 输出过长，为防止超过大模型上下文限制，已自动隐去中间的 %d 个字符；"+
			"如需完整内容，请缩小查询范围后重新调用该工具] ...\n\n", omitted)
}
