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

package agentbuilder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// toolErrorMessage is the prefix of the model-visible text a failed tool call
// is converted into. The model reads it as ordinary tool output and may retry,
// choose another route, or explain the failure in its answer.
const toolErrorMessage = "tool execution failed: %s"

// ErrToolCallRefused marks a failure that must fail the run instead of being
// handed to the model as tool output.
//
// A gate that cannot pause is the case it exists for: the call was refused
// because the deployment (or the request) has nowhere to ask a human. That is
// not information the model can act on — it would read "requires approval" as a
// tool that malfunctioned and carry on — so the run has to stop where a caller
// can decide what to do about the configuration.
var ErrToolCallRefused = errors.New("tool call refused")

// IsInterrupt reports whether err is eino's interrupt signal rather than a
// genuine failure. Both checks are needed: an approval pause raised through
// tool.StatefulInterrupt surfaces as an interrupt-rerun error, while one
// raised deeper in a composed graph only shows up in the extracted info.
//
// It is exported because anything that converts tool errors into text must
// never swallow an interrupt: doing so turns a pending human approval into a
// tool failure, and the run continues as if the user had refused.
func IsInterrupt(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := compose.IsInterruptRerunError(err); ok {
		return true
	}
	_, ok := compose.ExtractInterruptInfo(err)
	return ok
}

// ToolErrorHandler converts a failed tool call into a tool-role message whose
// content is the error string. Without it, the agent's tool node terminates
// the whole reasoning loop on the first tool error with a fatal event; with
// it, the failure is just information the model can act on.
//
// It is deliberately a middleware rather than a per-tool wrapper: the tool set
// is assembled per run, and wrapping at the BaseTool level would hide the
// original error from every middleware inside this one — including the tool
// tracer, which renders a failed call as ❌ and would otherwise see a
// successful text result. Register it FIRST in the tool-handler chain so it is
// the outermost wrapper.
type ToolErrorHandler struct {
	*adk.BaseChatModelAgentMiddleware
}

// NewToolErrorHandler returns the handler. A nil receiver is safe: the embedded
// base middleware supplies the no-op implementations.
func NewToolErrorHandler() *ToolErrorHandler {
	return &ToolErrorHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}}
}

// WrapInvokableToolCall converts a synchronous tool failure into model-visible text.
func (h *ToolErrorHandler) WrapInvokableToolCall(
	_ context.Context, endpoint adk.InvokableToolCallEndpoint, _ *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err == nil || IsInterrupt(err) || errors.Is(err, ErrToolCallRefused) {
			return result, err
		}
		slog.WarnContext(ctx, "tool execution failed", "error", err)
		return fmt.Sprintf(toolErrorMessage, err.Error()), nil
	}, nil
}

// WrapEnhancedInvokableToolCall converts an enhanced tool failure into a
// model-visible text result, keeping the structured shape the call returns.
//
// It mirrors the plain path for the same reason: without it, the first failure
// of a multimodal tool — a file too large to send, a format the model cannot
// read — would end the reasoning loop instead of being an error the model can
// work around by, say, reading a different page.
func (h *ToolErrorHandler) WrapEnhancedInvokableToolCall(
	_ context.Context, endpoint adk.EnhancedInvokableToolCallEndpoint, _ *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		result, err := endpoint(ctx, argument, opts...)
		if err == nil || IsInterrupt(err) || errors.Is(err, ErrToolCallRefused) {
			return result, err
		}
		slog.WarnContext(ctx, "tool execution failed", "error", err)
		return &schema.ToolResult{Parts: []schema.ToolOutputPart{{
			Type: schema.ToolPartTypeText,
			Text: fmt.Sprintf(toolErrorMessage, err.Error()),
		}}}, nil
	}, nil
}

// WrapStreamableToolCall converts a streaming tool failure into a one-chunk
// stream, and appends the same text if the stream breaks mid-flight.
func (h *ToolErrorHandler) WrapStreamableToolCall(
	_ context.Context, endpoint adk.StreamableToolCallEndpoint, _ *adk.ToolContext,
) (adk.StreamableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		stream, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			if IsInterrupt(err) || errors.Is(err, ErrToolCallRefused) {
				return nil, err
			}
			slog.WarnContext(ctx, "tool execution failed", "error", err)
			return singleChunkReader(fmt.Sprintf(toolErrorMessage, err.Error())), nil
		}
		return errorTextAppendingReader(ctx, stream), nil
	}, nil
}

// singleChunkReader returns a one-element stream carrying msg.
func singleChunkReader(message string) *schema.StreamReader[string] {
	r, w := schema.Pipe[string](1)
	_ = w.Send(message, nil)
	w.Close()
	return r
}

// errorTextAppendingReader forwards every chunk of sr and, if the stream ends
// with an error instead of EOF, appends the error as a final chunk so the
// model sees it too.
func errorTextAppendingReader(ctx context.Context, sr *schema.StreamReader[string]) *schema.StreamReader[string] {
	r, w := schema.Pipe[string](64)
	go func() {
		defer w.Close()
		for {
			chunk, err := sr.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				slog.WarnContext(ctx, "tool stream failed mid-flight", "error", err)
				_ = w.Send(fmt.Sprintf("\n"+toolErrorMessage, err.Error()), nil)
				return
			}
			_ = w.Send(chunk, nil)
		}
	}()
	return r
}
