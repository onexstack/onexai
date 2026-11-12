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
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/agent/pkg/agentbuilder"
	"github.com/onexstack/onexai/internal/agent/pkg/approval"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// approvalHandler pauses a tool call the policy gates, and runs it once a human
// has approved it.
//
// It hooks the agent's tool node rather than wrapping each tool, so every tool
// of a run is judged the same way: the built-ins this repo controls, and a
// third-party MCP server's tools, which it does not control and cannot edit.
// That is the whole point of putting the decision here — an MCP tool joins the
// approval flow by having its name listed in a resource, with no code change to
// the tool, to the tool list, or to the server that serves it.
//
// The decision itself lives in approval.Policy; this type is only the point of
// enforcement.
type approvalHandler struct {
	*adk.BaseChatModelAgentMiddleware
	// The four fields below are this handler's own run state, used when the
	// call's context carries none. That is the case for a handler driven
	// directly, as the tests do. A handler built for a reusable (cached) runner
	// leaves them at their zero values and relies entirely on the context, which
	// is what production attaches on every run.
	defaults runState
}

var _ adk.ChatModelAgentMiddleware = (*approvalHandler)(nil)

// newApprovalHandler returns the handler. Everything that varies between runs —
// the policy, whether a human already said "allow all", what an unanswerable
// gate does, and the authorization gate — may equally be carried on the run's
// context (see runState), in which case it takes precedence over these.
func newApprovalHandler(policy approval.Policy, allowAll bool, fallback string) *approvalHandler {
	return &approvalHandler{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		defaults:                     runState{policy: policy, allowAll: allowAll, fallback: fallback},
	}
}

// withAuthz attaches the run's authorization gate. It is a separate method
// rather than a constructor parameter so every existing call site and test
// keeps compiling unchanged, and so "no authz" stays the default spelling.
func (h *approvalHandler) withAuthz(g *authzGate) *approvalHandler {
	h.defaults.gate = g
	return h
}

// stateFor returns the state governing this call: the run's, when the context
// carries one, and otherwise this handler's own.
func (h *approvalHandler) stateFor(ctx context.Context) runState {
	return ptr.FromOr(runStateFrom(ctx), h.defaults)
}

// gated reports whether a call to toolName must not run until it is approved,
// on the static policy alone. It is the cheap pre-check used by the paths that
// cannot pause: those have no arguments to extract a resource from, so the
// authorization gate is consulted separately there (see needsApproval).
func (h *approvalHandler) gated(ctx context.Context, toolName string) bool {
	state := h.stateFor(ctx)
	if state.allowAll {
		return false
	}
	return state.policy.Decide(toolName) == approval.DecisionAsk
}

// needsApproval decides whether this call must be answered by a human before it
// runs, and why.
//
// Two independent things can hold a call: the static approval policy ("this tool
// is dangerous, always ask"), and authorization ("this caller may not invoke
// this tool, ask a human whether to elevate"). They are decided in that order —
// the policy check is free, the authorization check is not, and a call the
// policy already gates needs no authorization verdict to be held.
//
// A nil gate preserves the previous behaviour exactly: policy and allowAll
// only. allowAll short-circuits both, because a human answering "allow all"
// has already taken responsibility for the rest of this run.
func (h *approvalHandler) needsApproval(ctx context.Context, tCtx *adk.ToolContext, argumentsInJSON string) (ask bool, reason, source string) {
	state := h.stateFor(ctx)
	if state.allowAll {
		return false, "", ""
	}
	name := toolNameOf(tCtx)
	if state.policy.Decide(name) == approval.DecisionAsk {
		return true, "", agentapi.SourcePolicy
	}
	if state.gate == nil {
		return false, "", ""
	}

	allowed, reason, err := state.gate.authorize(ctx, tCtx, argumentsInJSON)
	if err != nil {
		// An authorization engine that cannot answer is not an approval. Hold
		// the call and say why, so an unreachable backend becomes a question to
		// a human rather than a silent grant.
		slog.WarnContext(ctx, "authz evaluation failed; holding the tool call for a human",
			"tool", name, "error", err)
		return true, fmt.Sprintf("权限校验失败：%v", err), agentapi.SourceAuthz
	}
	if !allowed {
		return true, reason, agentapi.SourceAuthz
	}
	return false, "", ""
}

// WrapInvokableToolCall is where a gated call pauses and where the answer to a
// paused one is applied.
//
// An ungated call is handed back as the very same endpoint, so a deployment that
// gates nothing pays for this middleware with nothing more than one decision per
// call. With an authorization gate attached that fast path is gone: the gate's
// verdict depends on the call's arguments and the caller's identity, neither of
// which is known until the call happens.
func (h *approvalHandler) WrapInvokableToolCall(
	_ context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		return gateCall(h, ctx, argumentsInJSON, tCtx, endpoint, func(s string) string { return s }, opts...)
	}, nil
}

// gateCall runs one tool call through the approval gate.
//
// It is generic over the result type because the same decision has to cover both
// the plain string-returning tool and the enhanced multimodal one. One body is
// what keeps the enhanced path from becoming a second, forgotten copy of a
// safety check: until multimodal read_file existed, nothing exercised it, and a
// separate implementation would have been the copy nobody updated.
//
// textResult adapts a refusal message to whichever type the caller returns; a
// refusal is an answer, not a failure, so it must reach the model in-band.
func gateCall[T any](
	h *approvalHandler, ctx context.Context, argumentsInJSON string, tCtx *adk.ToolContext,
	endpoint func(context.Context, string, ...tool.Option) (T, error),
	textResult func(string) T,
	opts ...tool.Option,
) (T, error) {
	var zero T
	// The ungated fast path is decided per call rather than at wrap time,
	// because the policy it consults travels on the run's context: the context
	// available to the wrapper is the one that built the graph, not the one the
	// call runs under.
	name := toolNameOf(tCtx)
	if h.stateFor(ctx).gate == nil && !h.gated(ctx, name) {
		return endpoint(ctx, argumentsInJSON, opts...)
	}

	wasInterrupted, hasState, storedArguments := tool.GetInterruptState[string](ctx)
	if !wasInterrupted {
		ask, reason, source := h.needsApproval(ctx, tCtx, argumentsInJSON)
		if !ask {
			return endpoint(ctx, argumentsInJSON, opts...)
		}

		// A run whose caller cannot answer never reaches the pause below: an
		// interrupt raised here would end the stream with a question nobody can
		// reply to, and the caller would receive an empty answer for a request
		// that did nothing. Refusing and failing are the two honest forms of
		// that answer; which one is a policy of the surface, not of the gate.
		fallback := h.stateFor(ctx).fallback
		if fallback != "" {
			if fallback == agentapi.ApprovalFallbackError {
				return zero, errUnanswered(name)
			}
			return textResult(refused(name, nil)), nil
		}

		return zero, tool.StatefulInterrupt(ctx,
			describeCall(tCtx, argumentsInJSON, reason, source), argumentsInJSON)
	}

	// Not every interrupt is ours.
	//
	// # Why this guard has to exist
	//
	// Once an authorization gate is attached this middleware wraps *every* tool
	// call, and `ask_user_question` raises an interrupt of its own — one this
	// middleware never created. Its resume data is the user's answer (a string),
	// so the permission branch below reads `hasData == false` and answers
	// `refused` — the model is told the person **refused the tool** moments
	// after they answered it, the tool never runs, and the answer they typed is
	// dropped. That is a whole feature (asking a question) inverted into a lie
	// about the user.
	//
	// The two kinds are told apart by the state we pause with: our pauses always
	// store the arguments as a `string` state, and a tool that interrupts on its
	// own stores none. `hasState` alone is not enough — a state that did not
	// survive the checkpoint looks the same — so the second half re-derives
	// whether this call is one we would gate at all. If it is not, the interrupt
	// cannot be ours, and the call belongs to the tool (whose own resume branch
	// reads the answer). If it is, the ambiguity stays and the fail-closed
	// answer below is kept.
	if !hasState {
		if ask, _, _ := h.needsApproval(ctx, tCtx, argumentsInJSON); !ask {
			return endpoint(ctx, argumentsInJSON, opts...)
		}
	}

	// State that did not survive the checkpoint would resume the tool with a
	// truncated argument string, so fall back to what the call carries.
	if !hasState || storedArguments == "" {
		storedArguments = argumentsInJSON
	}

	isTarget, hasData, result := tool.GetResumeContext[*agentapi.ApprovalResult](ctx)
	switch {
	case isTarget && hasData && result != nil && result.Approved:
		// A human's approval is the elevation: the call runs with the arguments
		// it paused with, and the tool itself never learns that authorization
		// had held it.
		return endpoint(ctx, storedArguments, opts...)

	case isTarget && hasData:
		// A refusal is an answer, not a failure. The model is told the tool did
		// not run and why, and closes the turn in its own words — which is the
		// only way to explain a refusal to the person who asked for the work.
		return textResult(refused(name, result)), nil

	case isTarget:
		// Named as the target but carrying no answer. This engine always resumes
		// a permission address with an ApprovalResult, so reaching here means
		// the answer was lost or malformed; running the tool would be acting
		// without one.
		return textResult(refused(name, nil)), nil

	default:
		// Not our turn. A sibling was answered instead, and proceeding here
		// would run a call no one approved; re-interrupting keeps this one
		// pending so a later answer can still reach it. eino requires exactly
		// this of an untargeted leaf.
		_, reason, source := h.needsApproval(ctx, tCtx, storedArguments)
		return zero, tool.StatefulInterrupt(ctx,
			describeCall(tCtx, storedArguments, reason, source), storedArguments)
	}
}

// WrapStreamableToolCall and WrapEnhancedStreamableToolCall refuse to run a
// gated tool down a path that cannot pause for approval.
//
// No tool in this deployment streams — the built-ins and the MCP tools are all
// synchronous — so these exist to keep that from becoming a way around the gate.
// Without them, adding a streamable tool to a server whose name matches an
// approval pattern would run it unapproved and silently: the tool would appear
// to work, and the approval list would appear to have been ignored. Failing the
// call is the loud version of the same event.
//
// The enhanced *invokable* path is not refused: it is gated by the same decision
// as the plain one (see WrapEnhancedInvokableToolCall), which is what lets the
// multimodal read_file tool — the reason that path exists — stay inside the
// approval flow.
func (h *approvalHandler) WrapStreamableToolCall(
	ctx context.Context, endpoint adk.StreamableToolCallEndpoint, tCtx *adk.ToolContext,
) (adk.StreamableToolCallEndpoint, error) {
	if ask, _, _ := h.needsApproval(ctx, tCtx, ""); !ask {
		return endpoint, nil
	}
	return nil, errUngateable(toolNameOf(tCtx), "streamable")
}

func (h *approvalHandler) WrapEnhancedInvokableToolCall(
	_ context.Context, endpoint adk.EnhancedInvokableToolCallEndpoint, tCtx *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
	// Enhanced tools return a structured result (text, images, files) rather
	// than a string. The gate is the same; only the shape of a refusal differs.
	return func(ctx context.Context, argument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		arguments := ""
		if argument != nil {
			arguments = argument.Text
		}
		return gateCall(h, ctx, arguments, tCtx,
			func(ctx context.Context, args string, opts ...tool.Option) (*schema.ToolResult, error) {
				return endpoint(ctx, &schema.ToolArgument{Text: args}, opts...)
			},
			textToolResult, opts...)
	}, nil
}

// textToolResult wraps a refusal in the shape an enhanced tool returns. A
// refusal has to reach the model in-band, exactly as it does for a plain tool.
func textToolResult(s string) *schema.ToolResult {
	return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: s}}}
}

func (h *approvalHandler) WrapEnhancedStreamableToolCall(
	ctx context.Context, endpoint adk.EnhancedStreamableToolCallEndpoint, tCtx *adk.ToolContext,
) (adk.EnhancedStreamableToolCallEndpoint, error) {
	if ask, _, _ := h.needsApproval(ctx, tCtx, ""); !ask {
		return endpoint, nil
	}
	return nil, errUngateable(toolNameOf(tCtx), "enhanced streamable")
}

// toolNameOf reads the tool name from the middleware's context, tolerating the
// nil that adk's own tests pass.
func toolNameOf(tCtx *adk.ToolContext) string {
	if tCtx == nil {
		return ""
	}
	return tCtx.Name
}

// describeCall builds the payload the interrupt shows a human. reason and
// source say why the call was held; both are empty for a plain policy gate.
func describeCall(tCtx *adk.ToolContext, argumentsInJSON, reason, source string) *agentapi.PermissionInfo {
	info := &agentapi.PermissionInfo{Arguments: argumentsInJSON, Reason: reason, Source: source}
	if tCtx != nil {
		info.ToolName, info.CallID = tCtx.Name, tCtx.CallID
	}
	return info
}

// refused is the tool result a denied call returns in place of running. It is a
// result rather than an error because the refusal is part of the conversation:
// the model reads it, explains it, and the turn ends normally.
func refused(toolName string, result *agentapi.ApprovalResult) string {
	if result != nil && result.Reason != "" {
		return fmt.Sprintf("用户拒绝执行工具 %s，原因：%s。不要重试该工具，请向用户说明并结束本轮。",
			toolName, result.Reason)
	}
	return fmt.Sprintf("用户拒绝执行工具 %s。不要重试该工具，请向用户说明并结束本轮。", toolName)
}

// errUngateable reports a tool that must be approved but cannot pause.
//
// It wraps ErrToolCallRefused so the tool-error handler lets it fail the run
// instead of converting it into text the model would read as a tool glitch and
// talk its way past.
func errUngateable(toolName, kind string) error {
	return fmt.Errorf("%w: tool %q requires approval but is a %s tool, which cannot pause for it; "+
		"remove %q from the approval list or make it an invokable tool",
		agentbuilder.ErrToolCallRefused, toolName, kind, toolName)
}

// errUnanswered is what a gated call fails with on a surface that cannot ask.
// Unlike a refusal this is not the model's to explain away: the caller asked for
// work that this request was not allowed to do, and only the caller can decide
// what to do about that.
func errUnanswered(toolName string) error {
	return fmt.Errorf("%w: tool %q requires a human's approval and this request has no way to ask for one",
		agentbuilder.ErrToolCallRefused, toolName)
}
