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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/pkg/agentbuilder"
	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// maxToolResultBytes bounds how much of a tool's output travels with its step.
//
// The output reaches the model in full regardless; this only caps the copy that
// is streamed to a caller and rendered on a card, where a directory listing or a
// log tail would otherwise ride on every event of the run. The figure is chosen
// to be comfortably above what a caller displays, so the renderer's own
// truncation — which knows the width it has — is what a reader sees, and this
// cut only ever applies to output that was never going to fit anywhere.
const maxToolResultBytes = 4096

// toolTraceHandler reports every tool invocation as a pair of tool steps: a
// pending one when the call starts, and a terminal one carrying the measured
// duration when it returns.
//
// It hooks the agent's tool node rather than wrapping each tool because the tool
// set is assembled per run — the built-ins plus the selected agents' MCP tools —
// and one middleware covers all of them. It is also the only vantage point from
// which a failed call is visible: adk's own tool event sender returns without
// emitting anything when the endpoint errors, so today a failed tool shows no ❌
// on the card at all.
type toolTraceHandler struct {
	*adk.BaseChatModelAgentMiddleware
	// emit is this handler's own sink, used when the call's context carries no
	// run state. A handler built for a reusable (cached) runner leaves it nil
	// and relies entirely on the context, which production attaches per run.
	emit *runEmitter
	// now is injectable so the duration can be asserted in tests.
	now func() time.Time
	// timeout, when positive, bounds each invocation's context.
	timeout time.Duration
	// maxResultBytes bounds the tool output carried on a step. It is a field so a
	// test can assert the cut without producing a result of the default size.
	maxResultBytes int
}

// newToolTraceHandler returns the handler. The run whose steps it reports is
// read from the call's context (see runState), which is what lets one handler
// instance serve every run of the agent set it was built for.
func newToolTraceHandler(emit *runEmitter, timeout time.Duration) *toolTraceHandler {
	return &toolTraceHandler{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		emit:                         emit,
		now:                          time.Now,
		timeout:                      timeout,
		maxResultBytes:               maxToolResultBytes,
	}
}

// emitStep publishes one tool step to this run's sink.
//
// The sink comes from the call's context when the run attached one, and
// otherwise from the handler's own field — which is how a handler driven
// directly, as the tests do, still reports. A run with no sink at all still
// times and reports its tools to the model; it simply has nowhere to publish
// the steps, which is what a caller that renders none gets.
func (h *toolTraceHandler) emitStep(ctx context.Context, step agentapi.ToolStep) {
	emit := h.emit
	if state := runStateFrom(ctx); state != nil {
		emit = state.emitter
	}
	if emit == nil {
		return
	}
	emit.send(agentapi.Event{Type: agentapi.EventToolStep, Steps: []agentapi.ToolStep{step}})
}

// WrapInvokableToolCall times one synchronous tool call around the endpoint.
func (h *toolTraceHandler) WrapInvokableToolCall(
	_ context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	var name, callID string
	if tCtx != nil {
		name, callID = tCtx.Name, tCtx.CallID
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		ctx, start, cancel := h.begin(ctx, callID, name)
		defer cancel()

		out, err := endpoint(ctx, argumentsInJSON, opts...)

		// A call paused for human approval comes back as an error, but the run
		// has not finished with it: it resumes under the same call ID once the
		// user answers. Reporting a failure here would put a ❌ on the card that
		// the resume then contradicts, so the step stays pending until then.
		if agentbuilder.IsInterrupt(err) {
			return out, err
		}
		h.end(ctx, callID, name, start, toolResult(out, err, h.maxResultBytes), err)
		return out, err
	}, nil
}

// WrapEnhancedInvokableToolCall times one enhanced tool call, whose result is
// structured rather than a string.
//
// It exists so the multimodal read_file tool reports the same step the plain
// tools do: a tool that returns an image would otherwise run invisibly, and a
// card would show a turn that used no tools while the model was looking at one.
func (h *toolTraceHandler) WrapEnhancedInvokableToolCall(
	_ context.Context, endpoint adk.EnhancedInvokableToolCallEndpoint, tCtx *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
	var name, callID string
	if tCtx != nil {
		name, callID = tCtx.Name, tCtx.CallID
	}

	return func(ctx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		ctx, start, cancel := h.begin(ctx, callID, name)
		defer cancel()

		out, err := endpoint(ctx, argument, opts...)
		if agentbuilder.IsInterrupt(err) {
			return out, err
		}
		h.end(ctx, callID, name, start, enhancedToolResult(out, err, h.maxResultBytes), err)
		return out, err
	}, nil
}

// begin emits the pending step and applies this call's own timeout.
func (h *toolTraceHandler) begin(ctx context.Context, callID, name string) (context.Context, time.Time, func()) {
	h.emitStep(ctx, agentapi.ToolStep{ID: callID, Name: name, Status: "pending"})
	if h.timeout <= 0 {
		return ctx, h.now(), func() {}
	}
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	// Mark the deadline as this call's own. Without it, a timeout cannot be told
	// apart from the run running out of time — and a tool's server health must
	// never be inferred from the latter.
	ctx = contextx.WithToolTimeout(ctx)
	return ctx, h.now(), cancel
}

// end records the outcome and emits the terminal step.
func (h *toolTraceHandler) end(
	ctx context.Context, callID, name string, start time.Time, result string, err error,
) {
	elapsed := h.now().Sub(start)
	status := "ok"
	if err != nil {
		status = "fail"
	}
	metrics.M.ObserveTool(name, status, elapsed)
	h.emitStep(ctx, agentapi.ToolStep{
		ID: callID, Name: name, Status: status, Duration: roundUpToMillis(elapsed),
		Result: result,
	})
}

// enhancedToolResult flattens a structured result for the card.
//
// The step carries a short text summary, never the bytes: an image part is
// described by its presence, and a text part is bounded exactly as a plain
// tool's output is.
func enhancedToolResult(out *schema.ToolResult, err error, max int) string {
	var text strings.Builder
	if out != nil {
		for _, part := range out.Parts {
			if part.Type != schema.ToolPartTypeText {
				if text.Len() > 0 {
					text.WriteString(" ")
				}
				text.WriteString("[" + string(part.Type) + "]")
				continue
			}
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(part.Text)
		}
	}
	return toolResult(text.String(), err, max)
}

// toolResult prepares a tool's output for the step that reports it.
//
// The output is returned to the model in full either way; this only decides what
// travels alongside the step, which is carried on every stream event of a run
// and rendered on a card. So it is bounded twice over: a tool that returns a
// directory listing or a log tail must not put it on the wire, and a failure
// carries its message because the error text is the only place a scripted tool
// explains itself — the endpoint's return value on the failure path is
// frequently empty.
//
// The cut is by bytes and marked, so a truncated result cannot be mistaken for
// the whole of one.
func toolResult(out string, err error, max int) string {
	text := out
	if err != nil {
		if message := strings.TrimSpace(err.Error()); message != "" {
			if strings.TrimSpace(text) == "" {
				text = message
			} else {
				text = strings.TrimSpace(text) + "\n" + message
			}
		}
	}
	text = strings.TrimSpace(text)
	if max <= 0 || len(text) <= max {
		return text
	}
	// Trim to the budget, then back off to a rune boundary: cutting a multi-byte
	// character in half would put invalid UTF-8 in a JSON field.
	cut := text[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n...（输出过长，已截断）"
}

// roundUpToMillis keeps a measured duration from being reported as zero.
//
// The figure is carried in whole milliseconds, and a zero means "this backend
// measures nothing" — the local CLI backend reports steps that way. A built-in
// tool that returns a constant finishes in microseconds, but truncating it to
// zero would erase the duration from the card entirely, so anything that
// genuinely took time is reported as at least a millisecond. The figure is a
// measurement, not an estimate; only its resolution is coarse.
func roundUpToMillis(d time.Duration) time.Duration {
	if d > 0 && d < time.Millisecond {
		return time.Millisecond
	}
	return d
}
