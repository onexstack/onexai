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

// Package customcommand runs user-defined slash commands. It is the REST-side
// executor: the chat service parses the command line with cobra, this package
// validates the structured params again and dispatches to prompt, http or tool.
package customcommand

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"google.golang.org/protobuf/types/known/structpb"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/approval"
	"github.com/onexstack/onexai/internal/agent/pkg/customcmd"
	"github.com/onexstack/onexai/internal/agent/pkg/mcpclient"
	"github.com/onexstack/onexai/internal/agent/pkg/metrics"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	"github.com/onexstack/onexai/internal/pkg/errmsg"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// CustomCommandBiz is the custom command service.
type CustomCommandBiz interface {
	// Create stores a new command definition.
	Create(ctx context.Context, req *v1.CreateCustomCommandRequest) (*v1.CreateCustomCommandResponse, error)
	// Get returns one command by name.
	Get(ctx context.Context, req *v1.GetCustomCommandRequest) (*v1.GetCustomCommandResponse, error)
	// List returns a page of command definitions.
	List(ctx context.Context, req *v1.ListCustomCommandRequest) (*v1.ListCustomCommandResponse, error)
	// Update rewrites one command definition.
	Update(ctx context.Context, req *v1.UpdateCustomCommandRequest) (*v1.UpdateCustomCommandResponse, error)
	// Delete removes one command definition by name.
	Delete(ctx context.Context, req *v1.DeleteCustomCommandRequest) (*v1.DeleteCustomCommandResponse, error)
	// Invoke runs one command. A deterministic refusal is reported in
	// status.phase=Denied, not as an error: the chat surface shows the reason.
	Invoke(ctx context.Context, name string, req *v1.InvokeCustomCommandRequest) (*v1.InvokeCustomCommandResponse, error)
	// ContextCommands lists what an agent exposes, for /help and for cobra.
	ContextCommands(ctx context.Context, agentName string) ([]*v1.ContextCustomCommand, error)
	// ListAudit returns the recorded invocation history.
	ListAudit(ctx context.Context, q AuditQuery) (int64, []*v1.CustomCommandAudit, error)
	// Approve answers a parked tool invocation.
	Approve(ctx context.Context, name string, req *v1.ApproveCustomCommandRequest) (*v1.ApproveCustomCommandResponse, error)
}

// Deps are the service's dependencies.
type Deps struct {
	Resources store.AIStore
	MCP       *mcpclient.Manager
	HTTP      *customcmd.HTTPExecutor
	// RequireApproval is the deployment-wide tool approval list. It is unioned
	// with the agent's and the MCP server's own lists.
	RequireApproval []string
	// Audit records invocations. Nil keeps the log and metric and skips the
	// history table.
	Audit AuditStore
	// Approvals parks tool calls that need a human. Nil makes such a call fail
	// with ApprovalUnavailable instead of running unapproved.
	Approvals ApprovalStore
}

// biz implements CustomCommandBiz.
type biz struct {
	deps Deps
}

var _ CustomCommandBiz = (*biz)(nil)

// New builds the custom command service.
func New(deps Deps) CustomCommandBiz { return &biz{deps: deps} }

// Invoke implements CustomCommandBiz.
//
// The wrapper exists for the audit trail: one line and one metric per call,
// whichever branch the invocation took. It records the command, agent, caller,
// conversation and outcome, never the parameters or the rendered body.
func (b *biz) Invoke(
	ctx context.Context, name string, req *v1.InvokeCustomCommandRequest,
) (*v1.InvokeCustomCommandResponse, error) {
	started := time.Now()
	resp, err := b.invoke(ctx, name, req)
	b.recordInvocation(ctx, name, req, resp, err, time.Since(started))
	return resp, err
}

func (b *biz) invoke(
	ctx context.Context, name string, req *v1.InvokeCustomCommandRequest,
) (*v1.InvokeCustomCommandResponse, error) {
	uid := req.GetUid()
	if uid == "" {
		uid = string(uuid.NewUUID())
	}
	if b.deps.Resources == nil {
		return denied(uid, name, "StoreDisabled", "resource store is not configured"), nil
	}

	command, err := b.deps.Resources.CustomCommands().Get(ctx, where.F("name", name))
	if err != nil || command == nil {
		return denied(uid, name, "CommandNotFound", fmt.Sprintf("custom command %q not found", name)), nil
	}
	if !command.Enabled() {
		return denied(uid, name, "CommandDisabled", fmt.Sprintf("custom command %q is disabled", name)), nil
	}

	agent, err := b.deps.Resources.Agents().Get(ctx, where.F("name", req.GetAgent()))
	if err != nil || agent == nil {
		return denied(uid, name, "AgentNotFound", fmt.Sprintf("agent %q not found", req.GetAgent())), nil
	}
	if !agent.Enabled() {
		return denied(uid, name, "AgentDisabled", fmt.Sprintf("agent %q is disabled", req.GetAgent())), nil
	}
	if !containsName(agent.EffectiveCustomCommands(), name) {
		return denied(uid, name, "NotBound",
			fmt.Sprintf("agent %q does not bind custom command %q", req.GetAgent(), name)), nil
	}

	spec, err := command.Spec()
	if err != nil {
		return nil, errno.ErrInternal.WithMessage("decoding custom command %q: %v", name, err)
	}
	params, err := customcmd.ValidateParams(spec.Parameters, paramsOf(req))
	if err != nil {
		return nil, errno.ErrInvalidArgument.WithMessage(
			"%v\n用法: %s", err, usageLine(name, spec.Parameters))
	}

	user := customcmd.UserInfoFromProto(req.GetUser())
	data := customcmd.TemplateData{
		Params:  params,
		Command: name,
		Agent:   req.GetAgent(),
		Conversation: map[string]string{
			"conversationId": req.GetContext().GetConversationId(),
			"chatType":       req.GetContext().GetChatType(),
			"surface":        req.GetContext().GetSurface(),
		},
		User:   user,
		DryRun: req.GetDryRun(),
		Now:    time.Now(),
	}

	resp := &v1.InvokeCustomCommandResponse{
		Uid:  uid,
		Kind: spec.Type,
		Name: name,
		Status: &v1.InvocationStatus{
			Phase:  "Succeeded",
			Usage:  usageLine(name, spec.Parameters),
			Params: structOf(params),
		},
	}

	switch spec.Type {
	case "prompt":
		b.invokePrompt(resp, spec, data)
	case "http":
		b.invokeHTTP(ctx, resp, spec, command, params, data, req.GetDryRun())
	case "tool":
		b.invokeTool(ctx, resp, spec, agent, command, params, data, req)
	default:
		fail(resp, "InvalidType", fmt.Sprintf("unsupported command type %q", spec.Type), "")
	}
	return resp, nil
}

func (b *biz) invokePrompt(
	resp *v1.InvokeCustomCommandResponse,
	spec *model.CustomCommandSpecM,
	data customcmd.TemplateData,
) {
	if spec.Prompt == nil {
		fail(resp, "InvalidDefinition", "command has no prompt body", "")
		return
	}
	text, err := customcmd.RenderTemplate("prompt", spec.Prompt.Template, data)
	if err != nil {
		fail(resp, "RenderFailed", errmsg.Text(err), "")
		return
	}
	resp.Status.Prompt = &v1.PromptInvocation{Prompt: text}
}

func (b *biz) invokeHTTP(
	ctx context.Context,
	resp *v1.InvokeCustomCommandResponse,
	spec *model.CustomCommandSpecM,
	command *model.CustomCommandM,
	params map[string]any,
	data customcmd.TemplateData,
	dryRun bool,
) {
	if spec.HTTP == nil {
		fail(resp, "InvalidDefinition", "command has no http body", "")
		return
	}
	if dryRun && !dryRunAllowed(spec.HTTP.SideEffects) {
		resp.Status.Phase = "Denied"
		resp.Status.Reason = "DryRunNotSupported"
		resp.Status.Message = "the endpoint does not declare sideEffects None or NoneOnDryRun"
		return
	}
	if b.deps.HTTP == nil {
		fail(resp, "HTTPDisabled", "http commands are not enabled on this deployment", "")
		return
	}
	bearer, err := b.bearer(ctx, spec.HTTP.SecretRef)
	if err != nil {
		fail(resp, "SecretUnavailable", errmsg.Text(err), "")
		return
	}

	started := time.Now()
	result, callErr := b.deps.HTTP.Execute(ctx, spec.HTTP, spec.Output, params, data,
		time.Duration(command.TimeoutSeconds)*time.Second, bearer)
	elapsed := time.Since(started).Milliseconds()
	if result != nil {
		resp.Status.Http = &v1.HTTPInvocation{
			Status:      int32(result.Status),
			ContentType: result.ContentType,
			Body:        boundedValue(result.Body),
			Rendered:    result.Rendered,
			ElapsedMs:   elapsed,
		}
	}
	if callErr != nil {
		var ce *customcmd.CallError
		if errors.As(callErr, &ce) {
			if ce.Evaluation {
				fail(resp, "EvaluationFailed", ce.Message, ce.Message)
			} else {
				fail(resp, "UpstreamError", ce.Message, "")
			}
			return
		}
		fail(resp, "Failed", callErr.Error(), "")
		return
	}
	resp.Status.Reason = "OK"
}

func (b *biz) invokeTool(
	ctx context.Context,
	resp *v1.InvokeCustomCommandResponse,
	spec *model.CustomCommandSpecM,
	agent *model.AgentM,
	command *model.CustomCommandM,
	params map[string]any,
	data customcmd.TemplateData,
	req *v1.InvokeCustomCommandRequest,
) {
	if spec.Tool == nil {
		fail(resp, "InvalidDefinition", "command has no tool body", "")
		return
	}
	if req.GetDryRun() {
		resp.Status.Phase = "Denied"
		resp.Status.Reason = "DryRunNotSupported"
		resp.Status.Message = "MCP tools have no dry-run semantics"
		return
	}
	server := spec.Tool.MCPServer.Name
	serverRef, bound := findRef(agent.EffectiveMCPs(), agent.Namespace, server)
	if !bound {
		resp.Status.Phase = "Denied"
		resp.Status.Reason = "MCPNotBound"
		resp.Status.Message = fmt.Sprintf("agent %q does not bind MCP server %q", req.GetAgent(), server)
		return
	}
	mcpRow, err := b.deps.Resources.MCPs().Get(ctx, serverRef.Filter())
	if err != nil || mcpRow == nil {
		resp.Status.Phase = "Denied"
		resp.Status.Reason = "MCPNotFound"
		resp.Status.Message = fmt.Sprintf("MCP server %q not found", server)
		return
	}
	if len(mcpRow.AllowedTools) > 0 && !containsString(mcpRow.AllowedTools, spec.Tool.Tool) {
		resp.Status.Phase = "Denied"
		resp.Status.Reason = "ToolNotAllowed"
		resp.Status.Message = fmt.Sprintf("tool %q is not in the allowlist of MCP server %q", spec.Tool.Tool, server)
		return
	}

	arguments := "{}"
	if strings.TrimSpace(spec.Tool.ArgumentsTemplate) != "" {
		rendered, err := customcmd.RenderTemplate("tool.arguments", spec.Tool.ArgumentsTemplate, data)
		if err != nil {
			fail(resp, "RenderFailed", errmsg.Text(err), "")
			return
		}
		arguments = rendered
	}
	if !json.Valid([]byte(arguments)) {
		fail(resp, "InvalidArguments", "argumentsTemplate did not render valid JSON", "")
		return
	}

	patterns := append([]string(nil), b.deps.RequireApproval...)
	patterns = append(patterns, agent.EffectiveRequireApproval()...)
	patterns = append(patterns, mcpRow.RequireApproval...)
	if approval.New(patterns, nil).Decide(spec.Tool.Tool) == approval.DecisionAsk {
		if req.GetContext().GetSurface() == "mcp" {
			resp.Status.Phase = "Denied"
			resp.Status.Reason = "ApprovalRequired"
			resp.Status.Message = fmt.Sprintf(
				"tool %q requires human approval; a synchronous MCP tool call has no approval UI", spec.Tool.Tool)
			return
		}
		b.parkToolApproval(ctx, resp, command, spec, req, server, arguments)
		return
	}

	b.runTool(ctx, resp.Status, serverRef.Canonical(), spec.Tool.Tool, arguments, req.GetAgent(), userIDOf(data))
}

// parkToolApproval stores the rendered call and returns PendingApproval.
func (b *biz) parkToolApproval(
	ctx context.Context,
	resp *v1.InvokeCustomCommandResponse,
	command *model.CustomCommandM,
	spec *model.CustomCommandSpecM,
	req *v1.InvokeCustomCommandRequest,
	server, arguments string,
) {
	if b.deps.Approvals == nil {
		fail(resp, "ApprovalUnavailable", "approval store is not configured on this deployment", "")
		return
	}
	now := time.Now()
	approvalID := string(uuid.NewUUID())
	row := &model.CustomCommandApprovalM{
		ApprovalID:     approvalID,
		UID:            resp.GetUid(),
		Command:        command.Name,
		Agent:          req.GetAgent(),
		UserID:         req.GetUser().GetUsername(),
		ConversationID: req.GetContext().GetConversationId(),
		Tool:           spec.Tool.Tool,
		MCPServer:      server,
		Arguments:      arguments,
		Phase:          approvalStatusPending,
		CreatedAt:      now.UnixMilli(),
		ExpiresAt:      now.Add(approvalTTL).UnixMilli(),
	}
	if err := b.deps.Approvals.Create(ctx, row); err != nil {
		fail(resp, "ApprovalUnavailable", errmsg.Text(err), "")
		return
	}
	resp.Status.Phase = "PendingApproval"
	resp.Status.Reason = "ApprovalRequired"
	resp.Status.Message = fmt.Sprintf("tool %q requires human approval", spec.Tool.Tool)
	resp.Status.Approval = &v1.CustomCommandApproval{
		ApprovalId:    approvalID,
		Command:       command.Name,
		Agent:         req.GetAgent(),
		Tool:          spec.Tool.Tool,
		McpServer:     server,
		ArgumentsJson: arguments,
		ExpiresAt:     row.ExpiresAt,
		Phase:         approvalStatusPending,
	}
}

// runTool performs one MCP call and fills status. It returns the raw result so
// an approval record can keep what the tool answered.
func (b *biz) runTool(
	ctx context.Context,
	status *v1.InvocationStatus,
	server, tool, arguments, agentName, userID string,
) string {
	if b.deps.MCP == nil {
		failStatus(status, "MCPDisabled", "MCP tools are not enabled on this deployment", "")
		return ""
	}
	callCtx := contextx.WithAgentNames(ctx, []string{agentName})
	if userID != "" {
		callCtx = contextx.WithUserID(callCtx, userID)
	}
	started := time.Now()
	result, err := b.deps.MCP.InvokeTool(callCtx, server, tool, arguments, []string{agentName})
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		failStatus(status, "ToolFailed", errmsg.Text(err), "")
		return ""
	}
	status.Tool = &v1.ToolInvocation{
		McpServer: server,
		Tool:      tool,
		Arguments: valueOfJSON([]byte(arguments)),
		Result:    valueOfJSONOrText([]byte(result)),
		ElapsedMs: elapsed,
	}
	status.Reason = "OK"
	return result
}

// Approve implements CustomCommandBiz.
func (b *biz) Approve(
	ctx context.Context, name string, req *v1.ApproveCustomCommandRequest,
) (*v1.ApproveCustomCommandResponse, error) {
	if b.deps.Approvals == nil {
		return nil, errno.ErrInternal.WithMessage("approval store is not configured")
	}
	decision := strings.ToLower(strings.TrimSpace(req.GetDecision()))
	if decision != "allow" && decision != "deny" {
		return nil, errno.ErrInvalidArgument.WithMessage(`decision must be "allow" or "deny"`)
	}

	row, err := b.deps.Approvals.Get(ctx, req.GetApprovalId())
	if err != nil {
		if errors.Is(err, ErrApprovalNotFound) {
			return nil, errno.ErrNotFound.WithMessage("approval %q not found", req.GetApprovalId())
		}
		return nil, errno.ErrInternal.WithMessage("%v", err)
	}
	if row.Command != name {
		return nil, errno.ErrNotFound.WithMessage(
			"approval %q does not belong to command %q", req.GetApprovalId(), name)
	}
	approver := req.GetUser().GetUsername()
	if row.Phase != approvalStatusPending {
		return approvalRefused(row, "ApprovalAlreadyDecided", "the approval has already been decided"), nil
	}
	now := time.Now()
	if now.UnixMilli() > row.ExpiresAt {
		row.Phase = "expired"
		row.DecidedAt = now.UnixMilli()
		row.DecidedBy = approver
		// The decision must be stored before it is reported: a write that failed
		// silently leaves the row pending, and the next click runs the tool.
		if err := b.deps.Approvals.Update(ctx, row); err != nil {
			return nil, errno.ErrInternal.WithMessage("recording the expired approval: %v", err)
		}
		return approvalRefused(row, "ApprovalExpired", "the approval window has expired"), nil
	}
	if req.GetAgent() != "" && row.Agent != req.GetAgent() {
		return approvalRefused(row, "ApprovalAgentMismatch", "the approval belongs to another agent"), nil
	}
	if row.UserID != "" && approver != row.UserID {
		return approvalRefused(row, "ApprovalUserMismatch",
			"only the person who ran the command may approve it"), nil
	}

	row.DecidedAt = now.UnixMilli()
	row.DecidedBy = approver
	if decision == "deny" {
		row.Phase = "denied"
		if err := b.deps.Approvals.Update(ctx, row); err != nil {
			return nil, errno.ErrInternal.WithMessage("recording the denied approval: %v", err)
		}
		return approvalRefused(row, "ApprovalDenied", "the approver denied the tool call"), nil
	}

	// Claim the approval before executing: a second click must not run the tool
	// a second time.
	row.Phase = "approved"
	if err := b.deps.Approvals.Update(ctx, row); err != nil {
		return nil, errno.ErrInternal.WithMessage("%v", err)
	}
	resp := &v1.ApproveCustomCommandResponse{
		ApprovalId: row.ApprovalID,
		Kind:       "tool",
		Name:       row.Command,
		Status:     &v1.InvocationStatus{Phase: "Succeeded"},
	}
	raw := b.runTool(ctx, resp.Status, row.MCPServer, row.Tool, row.Arguments, row.Agent, row.UserID)
	row.Result = customcmd.TruncateForChat(raw, 4096)
	if resp.Status.GetPhase() != "Succeeded" {
		row.Reason = resp.Status.GetReason() + ": " + resp.Status.GetMessage()
	}
	// The tool has already run. Losing the result must not turn a completed
	// call into an error for the caller, so this write is reported, not returned.
	if err := b.deps.Approvals.Update(ctx, row); err != nil {
		slog.WarnContext(ctx, "custom command approval: cannot record the tool result",
			"approval", row.ApprovalID, "error", err)
	}
	return resp, nil
}

func approvalRefused(
	row *model.CustomCommandApprovalM, reason, message string,
) *v1.ApproveCustomCommandResponse {
	return &v1.ApproveCustomCommandResponse{
		ApprovalId: row.ApprovalID,
		Kind:       "tool",
		Name:       row.Command,
		Status: &v1.InvocationStatus{
			Phase:   "Denied",
			Reason:  reason,
			Message: message,
		},
	}
}

func userIDOf(data customcmd.TemplateData) string {
	if data.User == nil {
		return ""
	}
	return data.User.Username
}

func (b *biz) bearer(ctx context.Context, ref *model.ReferenceM) (string, error) {
	if ref == nil || ref.Name == "" {
		return "", nil
	}
	secret, err := b.deps.Resources.Secrets().Get(ctx, where.F("secret_id", ref.Name))
	if err != nil || secret == nil {
		return "", fmt.Errorf("secret %q not found", ref.Name)
	}
	return secret.SecretKey, nil
}

// ContextCommands implements CustomCommandBiz.
func (b *biz) ContextCommands(ctx context.Context, agentName string) ([]*v1.ContextCustomCommand, error) {
	if b.deps.Resources == nil {
		return nil, errno.ErrInternal.WithMessage("resource store is not configured")
	}
	agent, err := b.deps.Resources.Agents().Get(ctx, where.F("name", agentName))
	if err != nil || agent == nil {
		return nil, errno.ErrNotFound.WithMessage("agent %q not found", agentName)
	}
	if !agent.Enabled() {
		return nil, nil
	}

	names := append([]string(nil), agent.EffectiveCustomCommands()...)
	sort.Strings(names)

	out := make([]*v1.ContextCustomCommand, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if strings.Contains(name, "/") {
			// The store is namespace-scoped; a qualified cross-namespace name is
			// not readable here and is skipped rather than resolved to a
			// same-named command in the wrong workspace.
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}

		row, err := b.deps.Resources.CustomCommands().Get(ctx, where.F("name", name))
		if err != nil || row == nil || !row.Enabled() {
			continue
		}
		item, err := customcmd.ContextToProto(row)
		if err != nil {
			return nil, errno.ErrInternal.WithMessage("decoding custom command %q: %v", name, err)
		}
		out = append(out, item)
	}
	return out, nil
}

// recordInvocation writes the audit line and updates the metric. A malformed
// request (err != nil) is recorded as "Error"; a deterministic refusal is
// "Denied"; the command's own execution is "Succeeded" or "Failed".
func (b *biz) recordInvocation(
	ctx context.Context,
	name string,
	req *v1.InvokeCustomCommandRequest,
	resp *v1.InvokeCustomCommandResponse,
	err error,
	elapsed time.Duration,
) {
	kind, phase, reason, uid := "", "", "", ""
	if resp != nil {
		uid = resp.GetUid()
		kind = resp.GetKind()
		phase = resp.GetStatus().GetPhase()
		reason = resp.GetStatus().GetReason()
	}
	switch {
	case err != nil:
		phase = "Error"
	case phase == "":
		phase = "Unknown"
	}
	metrics.M.ObserveCustomCommand(kind, phase, elapsed)
	slog.InfoContext(ctx, "custom command invoked",
		"uid", uid,
		"command", name,
		"agent", req.GetAgent(),
		"kind", kind,
		"phase", phase,
		"reason", reason,
		"conversation", req.GetContext().GetConversationId(),
		"surface", req.GetContext().GetSurface(),
		"user", req.GetUser().GetUsername(),
		"duration_ms", elapsed.Milliseconds(),
	)
	b.recordAudit(ctx, req, name, uid, kind, phase, reason, resp, elapsed)
}

// recordAudit writes the history row. A failed write is logged, never returned:
// the command already ran, and losing its audit row must not turn a success
// into an error for the caller.
func (b *biz) recordAudit(
	ctx context.Context,
	req *v1.InvokeCustomCommandRequest,
	name, uid, kind, phase, reason string,
	resp *v1.InvokeCustomCommandResponse,
	elapsed time.Duration,
) {
	if b.deps.Audit == nil {
		return
	}
	evaluationError := ""
	if resp != nil {
		evaluationError = resp.GetStatus().GetEvaluationError()
	}
	row := &model.CustomCommandAuditM{
		UID:             uid,
		Command:         name,
		Agent:           req.GetAgent(),
		Kind:            kind,
		Phase:           phase,
		Reason:          reason,
		UserID:          req.GetUser().GetUsername(),
		ConversationID:  req.GetContext().GetConversationId(),
		Surface:         req.GetContext().GetSurface(),
		DurationMs:      elapsed.Milliseconds(),
		EvaluationError: customcmd.TruncateForChat(evaluationError, 4096),
		CreatedAt:       time.Now().UnixMilli(),
	}
	// WithoutCancel: the caller disconnecting must not drop the audit of the
	// work that already happened. The short timeout is the only bound.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := b.deps.Audit.Write(writeCtx, row); err != nil {
		slog.WarnContext(ctx, "custom command audit write failed", "uid", uid, "error", err)
	}
}

// ListAudit implements CustomCommandBiz.
func (b *biz) ListAudit(
	ctx context.Context, q AuditQuery,
) (int64, []*v1.CustomCommandAudit, error) {
	if b.deps.Audit == nil {
		return 0, nil, nil
	}
	total, rows, err := b.deps.Audit.List(ctx, q)
	if err != nil {
		return 0, nil, errno.ErrInternal.WithMessage("%v", err)
	}
	out := make([]*v1.CustomCommandAudit, 0, len(rows))
	for _, row := range rows {
		out = append(out, auditToProto(row))
	}
	return total, out, nil
}

func auditToProto(row *model.CustomCommandAuditM) *v1.CustomCommandAudit {
	if row == nil {
		return nil
	}
	return &v1.CustomCommandAudit{
		Uid:             row.UID,
		Command:         row.Command,
		Agent:           row.Agent,
		Kind:            row.Kind,
		Phase:           row.Phase,
		Reason:          row.Reason,
		UserId:          row.UserID,
		ConversationId:  row.ConversationID,
		Surface:         row.Surface,
		DurationMs:      row.DurationMs,
		EvaluationError: row.EvaluationError,
		CreatedAt:       row.CreatedAt,
	}
}

// containsName reports whether a resolved set names this command.
//
// Only an unqualified entry matches. A "namespace/name" entry is a reference
// this store cannot resolve — it reads one namespace — so it is left out, which
// is exactly what ContextCommands does when it builds the menu. Matching its
// last segment here would let a cross-namespace grant be redeemed by a
// same-named local command.
func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

// findRef returns the reference in entries whose name is name. Entries are
// canonical "namespace/name"; a bare entry belongs to namespace.
//
// A reference in the agent's own namespace wins over a same-named one it binds
// from elsewhere, so an agent that binds both keeps its own.
func findRef(entries []string, namespace, name string) (model.Ref, bool) {
	var found model.Ref
	ok := false
	for _, entry := range entries {
		ref := model.ResolveRef(namespace, entry)
		if ref.Name != name {
			continue
		}
		if ref.Namespace == model.NormalizeNamespace(namespace) {
			return ref, true
		}
		if !ok {
			found, ok = ref, true
		}
	}
	return found, ok
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}

func paramsOf(req *v1.InvokeCustomCommandRequest) map[string]any {
	if req.GetParams() == nil {
		return map[string]any{}
	}
	return req.GetParams().AsMap()
}

func dryRunAllowed(sideEffects string) bool {
	return sideEffects == "None" || sideEffects == "NoneOnDryRun"
}

// usageLine is the server-side twin of the chat service's canonical usage. It
// is derived from the declarations so a stale spec.usage cannot make an
// argument error teach the caller the wrong flag.
func usageLine(name string, params []model.CommandParameterM) string {
	return customcmd.CanonicalUsage(name, params)
}

func denied(uid, name, reason, message string) *v1.InvokeCustomCommandResponse {
	return &v1.InvokeCustomCommandResponse{
		Uid:  uid,
		Name: name,
		Status: &v1.InvocationStatus{
			Phase:   "Denied",
			Reason:  reason,
			Message: message,
		},
	}
}

func fail(resp *v1.InvokeCustomCommandResponse, reason, message, evaluationError string) {
	failStatus(resp.Status, reason, message, evaluationError)
}

func failStatus(status *v1.InvocationStatus, reason, message, evaluationError string) {
	status.Phase = "Failed"
	status.Reason = reason
	status.Message = message
	status.EvaluationError = evaluationError
}

func structOf(params map[string]any) *structpb.Struct {
	out, err := structpb.NewStruct(params)
	if err != nil {
		return nil
	}
	return out
}

// boundedValue turns a response body into a proto value, bounding it first.
//
// The rendered form is what a surface shows, so the raw body is only context —
// and a 1 MiB body carried whole is a real risk against the gRPC message limit,
// which would fail the whole invocation rather than shorten one field.
func boundedValue(body []byte) *structpb.Value {
	if len(body) > customcmd.ResultLimit {
		return structpb.NewStringValue(
			customcmd.TruncateForChat(string(body), customcmd.ResultLimit))
	}
	return valueOfJSON(body)
}

func valueOfJSON(body []byte) *structpb.Value {
	if len(body) == 0 || !json.Valid(body) {
		return structpb.NewStringValue(string(body))
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return structpb.NewStringValue(string(body))
	}
	out, err := structpb.NewValue(v)
	if err != nil {
		return structpb.NewStringValue(string(body))
	}
	return out
}

func valueOfJSONOrText(body []byte) *structpb.Value {
	if len(body) == 0 {
		return structpb.NewStringValue("")
	}
	return valueOfJSON(body)
}
