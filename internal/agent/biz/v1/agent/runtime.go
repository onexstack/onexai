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
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/onexstack/onexai/internal/agent/capability"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// a2aSendRequest is the wire request for one turn. The text is the only part
// the A2A runtime needs to reason over; images travel through the same asset
// references the in-process path uses, once the A2A image parts are wired.
func a2aSendRequest(req *agentapi.AskRequest) *a2a.SendMessageRequest {
	return &a2a.SendMessageRequest{
		Message: &a2a.Message{
			ID:    req.RunID,
			Role:  a2a.MessageRoleUser,
			Parts: []*a2a.Part{a2a.NewTextPart(req.Input.Text)},
		},
		Metadata: map[string]any{
			runtime.MetadataRunID: req.RunID,
			runtime.MetadataAgent: req.Selection.Agents,
		},
	}
}

// shouldUsePort reports whether this run goes through the runtime port. A run
// that names a profile always does — that is the feature — while a run that
// names none keeps the historical Engine path unless the deployment turned the
// port on globally. A selection that names two different profiles is refused
// here, before any work is done.
func (b *AgentBiz) shouldUsePort(sel resolvedSelection) (bool, error) {
	if b.deps.Runtimes == nil {
		return false, nil
	}
	// A hostless run has no agent name to put in the runtime capability token,
	// and attachCapabilityToken returns nil for an empty selection. Sending the
	// run to the port would lose the composed MCP/skill lists, so it stays on
	// the in-process engine until the port path has a playground identity.
	if len(sel.set.names) == 0 {
		return false, nil
	}
	profile, err := sel.set.runtimeProfile()
	if err != nil {
		return false, err
	}
	if profile == "" {
		return b.deps.PortEnabled, nil
	}
	return true, nil
}

// dispatch resolves which runtime this run uses and hands it the pair of calls.
// Ask and Approve supply different pairs (Run/runViaPort against
// Resume/resumeViaPort); the choice itself is the same, so it lives here.
func (b *AgentBiz) dispatch(
	ctx context.Context, sel resolvedSelection,
	legacy, port func(context.Context) (<-chan agentapi.Event, error),
) (<-chan agentapi.Event, error) {
	usePort, err := b.shouldUsePort(sel)
	if err != nil {
		return nil, err
	}
	return b.dispatchRun(ctx, usePort, legacy, port)
}

// dispatchRun hands the run to the runtime the caller chose — through the
// runtime port when usePort is set, through the legacy engine otherwise — and
// publishes the image resolver on the context the engine receives.
//
// Ask and Approve differ only in which pair of calls the two branches make
// (Run/runViaPort against Resume/resumeViaPort), so the choice and the inliner
// live here. usePort is resolved by the caller rather than here because a
// selection that names conflicting profiles is refused with an error the
// caller reports on its own terms.
func (b *AgentBiz) dispatchRun(
	ctx context.Context, usePort bool,
	legacy, port func(context.Context) (<-chan agentapi.Event, error),
) (<-chan agentapi.Event, error) {
	// Resolved once, before either branch: a resumed run needs the inliner as
	// much as a fresh one, and a data URL here would be checkpointed.
	ctx = b.withImageInliner(ctx)
	if usePort {
		return port(ctx)
	}
	return legacy(ctx)
}

// firstAgent names the agent a profile-specific runtime is chosen for.
func firstAgent(sel resolvedSelection) string {
	if len(sel.set.names) == 0 {
		return ""
	}
	return sel.set.names[0]
}

// firstAgentNamespace returns the namespace of the first selected agent, which
// is what a shared Memory records as the writer's origin.
func firstAgentNamespace(sel resolvedSelection) string {
	name := firstAgent(sel)
	for _, record := range sel.set.records {
		if record != nil && record.Name == name {
			return record.Namespace
		}
	}
	return ""
}

// runtimeKindFor names the runtime a run executes on, for the run row.
func (b *AgentBiz) runtimeKindFor(sel resolvedSelection) string {
	// shouldUsePort forces a hostless run onto the in-process engine; reporting
	// a port kind here would make the run row describe a runtime that never ran.
	if len(sel.set.names) == 0 {
		if b.deps.Engine != nil {
			return b.deps.Engine.Name()
		}
		return ""
	}
	if b.deps.Runtimes != nil {
		if profile, err := sel.set.runtimeProfile(); err == nil {
			if profile != "" || b.deps.PortEnabled {
				if port, err := b.deps.Runtimes.LookupFor(profile, firstAgent(sel)); err == nil {
					return port.Kind()
				}
			}
		}
	}
	if b.deps.Engine != nil {
		return b.deps.Engine.Name()
	}
	return ""
}

// runViaPort executes one turn on the runtime the selection names.
func (b *AgentBiz) runViaPort(
	ctx context.Context, req *agentapi.AskRequest, sel resolvedSelection,
	messages []agentapi.Message, opts agentapi.RunOptions,
) (<-chan agentapi.Event, error) {
	profile, err := sel.set.runtimeProfile()
	if err != nil {
		return nil, err
	}
	port, err := b.deps.Runtimes.LookupFor(profile, firstAgent(sel))
	if err != nil {
		return nil, err
	}
	send := a2aSendRequest(req)
	if err := b.attachCapabilityToken(send, req, sel); err != nil {
		return nil, err
	}
	injectTrace(ctx, send)
	return port.Run(ctx, &runtime.Request{
		A2A:        send,
		SessionKey: req.SessionID,
		RunID:      req.RunID,
		Options:    opts,
		History:    messages,
	})
}

// attachCapabilityToken mints the per-run token a remote runtime uses for MCP
// capability calls. The scopes are least-privilege: a run that declares no
// skills cannot ask for one. The in-process adapter ignores the token, so an
// inline deployment pays only the cost of signing it.
func (b *AgentBiz) attachCapabilityToken(
	send *a2a.SendMessageRequest, req *agentapi.AskRequest, sel resolvedSelection,
) error {
	if b.deps.CapabilityIssuer == nil || send == nil {
		return nil
	}
	scopes := make([]string, 0, 4)
	if len(sel.spec.Skills) > 0 {
		scopes = append(scopes, "skill")
	}
	if len(sel.spec.KnowledgeBases) > 0 {
		scopes = append(scopes, "kb")
	}
	if len(sel.spec.CustomCommands) > 0 {
		scopes = append(scopes, "command")
	}
	if sel.spec.MemoryEnabled {
		scopes = append(scopes, "memory")
	}
	// Delegation is a declared authority: an agent that names no delegate is
	// never granted the tool, so it cannot be talked into calling one.
	if len(sel.spec.Delegates) > 0 {
		scopes = append(scopes, "agent")
	}
	if sel.spec.SessionTools {
		scopes = append(scopes, "session")
	}
	// Every run gets a task scope: a remote runtime persists its task through
	// the TaskStore even when it has no capability tools.
	scopes = append(scopes, "task")
	ttl := time.Hour
	if req.Options.Deadline > 0 && req.Options.Deadline+5*time.Minute < ttl {
		ttl = req.Options.Deadline + 5*time.Minute
	}
	agentName := ""
	if len(sel.set.names) > 0 {
		agentName = sel.set.names[0]
	}
	if agentName == "" {
		return nil
	}
	claims := capability.Claims{
		RunID:       req.RunID,
		SessionKey:  req.SessionID,
		Agent:       agentName,
		Subject:     req.Subject.ID,
		SubjectKind: req.Subject.Kind,
		Scopes:      scopes,
	}
	// A narrowed run tells the gateway exactly what it may serve. Without the
	// lists, a run that selected two of five skills would still hold the skill
	// scope and the gateway would resolve the agent's full set.
	if sel.narrowed {
		claims.Narrowed = true
		claims.Skills = sel.spec.Skills
		claims.KnowledgeBases = sel.spec.KnowledgeBases
		claims.CustomCommands = sel.spec.CustomCommands
	}
	// Lineage rides in the token rather than in the request text so a delegate
	// cannot widen its own chain: the depth is signed by the gateway, and the
	// next delegation reads it from here.
	if req.Lineage != nil {
		claims.RootRunID = req.Lineage.RootRunID
		claims.ParentRunID = req.Lineage.ParentRunID
		claims.Depth = req.Lineage.Depth
	}
	token, err := b.deps.CapabilityIssuer.Issue(claims, ttl)
	if err != nil {
		return fmt.Errorf("issue capability token: %w", err)
	}
	if send.Metadata == nil {
		send.Metadata = make(map[string]any, 1)
	}
	send.Metadata[runtime.MetadataCapabilityToken] = token
	return nil
}

// resumeViaPort answers a paused run through the same port. The answer travels
// in A2A metadata, which is what a remote runtime reads; the in-process adapter
// translates it back into the eino resume call.
func (b *AgentBiz) resumeViaPort(
	ctx context.Context, req *agentapi.ApproveRequest, sel resolvedSelection,
	sessionKey string, deadline time.Duration, opts agentapi.RunOptions,
) (<-chan agentapi.Event, error) {
	profile, err := sel.set.runtimeProfile()
	if err != nil {
		return nil, err
	}
	port, err := b.deps.Runtimes.LookupFor(profile, firstAgent(sel))
	if err != nil {
		return nil, err
	}
	send := &a2a.SendMessageRequest{
		Message: &a2a.Message{
			ID:    req.RunID,
			Role:  a2a.MessageRoleUser,
			Parts: []*a2a.Part{a2a.NewTextPart(req.Decision)},
		},
		Metadata: map[string]any{runtime.MetadataRunID: req.RunID},
	}
	if err := runtime.SetHITLResponse(send, runtime.HITLResponse{
		RequestID: req.RequestID,
		Decision:  req.Decision,
		Answers:   req.Answers,
		Reason:    req.Reason,
		AllowAll:  req.Decision == agentapi.DecisionAllowAll,
	}); err != nil {
		return nil, fmt.Errorf("build approval response: %w", err)
	}
	injectTrace(ctx, send)
	if err := b.attachCapabilityToken(send, &agentapi.AskRequest{
		RunID:     req.RunID,
		SessionID: sessionKey,
		Subject:   agentapi.Principal{},
		Options:   agentapi.ExecutionOptions{Deadline: deadline},
	}, sel); err != nil {
		return nil, err
	}
	return port.Run(ctx, &runtime.Request{
		A2A:        send,
		SessionKey: sessionKey,
		RunID:      req.RunID,
		Options:    opts,
	})
}
