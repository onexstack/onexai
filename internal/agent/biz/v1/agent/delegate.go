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
	"strings"

	"github.com/google/uuid"

	"github.com/onexstack/onexai/internal/agent/capability"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// sourceDelegate names runs started by another agent, for audit and hooks.
const sourceDelegate = "delegate"

// Delegator runs an agent as a tool for another run.
//
// It is deliberately thin: a delegation is an ordinary Ask on a different agent
// and session, so it inherits admission, authorization, quota, hooks, tracing
// and the runtime port without restating any of it. The only things it adds are
// the lineage on the request and the shape the tool call returns.
type Delegator struct {
	// Biz is the same service every surface calls.
	Biz agentapi.IAgentBiz
}

var _ capability.AgentInvoker = (*Delegator)(nil)

// NewDelegator builds the invoker.
func NewDelegator(biz agentapi.IAgentBiz) *Delegator {
	return &Delegator{Biz: biz}
}

// Invoke implements capability.AgentInvoker.
func (d *Delegator) Invoke(ctx context.Context, call capability.AgentCall) (capability.AgentResult, error) {
	if d.Biz == nil {
		return capability.AgentResult{}, errno.ErrNotConfigured.WithMessage("delegation is not configured")
	}
	session := call.Session
	if session == "" {
		session = delegateSession(call.ParentSession, call.Agent)
	}
	if call.Answer != nil && call.TaskID != "" {
		return d.resume(ctx, call, session)
	}
	req := &agentapi.AskRequest{
		SessionID: session,
		RunID:     uuid.NewString(),
		Input:     agentapi.Input{Text: call.Message},
		Selection: agentapi.Selection{Agents: []string{call.Agent}},
		Subject: agentapi.Principal{
			ID:   call.Subject,
			Name: call.Subject,
			Kind: call.SubjectKind,
		},
		// A delegate may need a decision. Interactive makes it pause instead of
		// silently refusing, and the pause travels back to the caller as the
		// tool result, which is what lets the parent chain answer it.
		Options: agentapi.ExecutionOptions{
			Approval: agentapi.Approval{Channel: agentapi.ApprovalChannelInteractive},
		},
		Source: sourceDelegate,
		Lineage: &agentapi.Lineage{
			RootRunID:   call.RootRunID,
			ParentRunID: call.ParentRunID,
			Depth:       call.Depth,
		},
	}
	events, err := d.Biz.Ask(ctx, req)
	if err != nil {
		return capability.AgentResult{}, err
	}
	return collect(ctx, req.RunID, session, events)
}

// resume answers a paused delegate run. The resumed run keeps the lineage it
// was started with, so a chain stays one chain across pauses.
func (d *Delegator) resume(
	ctx context.Context, call capability.AgentCall, session string,
) (capability.AgentResult, error) {
	decision := call.Answer.Decision
	if decision == "" {
		decision = agentapi.DecisionAllow
	}
	if call.Answer.AllowAll {
		decision = agentapi.DecisionAllowAll
	}
	events, err := d.Biz.Approve(ctx, &agentapi.ApproveRequest{
		RunID:     call.TaskID,
		RequestID: call.Answer.RequestID,
		Decision:  decision,
	})
	if err != nil {
		return capability.AgentResult{}, err
	}
	return collect(ctx, call.TaskID, session, events)
}

// delegationFor is the identity a run's delegated tool calls speak for. Nil
// when the selection names no delegate, which is also what keeps the tool out
// of the run's graph.
func delegationFor(req *agentapi.AskRequest, sel resolvedSelection) *agentapi.DelegationContext {
	if len(sel.spec.Delegates) == 0 {
		return nil
	}
	delegation := &agentapi.DelegationContext{
		Agents:        sel.spec.Delegates,
		ParentSession: req.SessionID,
		Subject:       req.Subject.ID,
		SubjectKind:   req.Subject.Kind,
	}
	if req.Lineage != nil {
		delegation.RootRunID = req.Lineage.RootRunID
		delegation.Depth = req.Lineage.Depth
	}
	return delegation
}

// resumeDelegation rebuilds the interrupted run's delegation from its record.
func resumeDelegation(meta agentapi.RunMeta, sel resolvedSelection) *agentapi.DelegationContext {
	if len(sel.spec.Delegates) == 0 {
		return nil
	}
	delegation := &agentapi.DelegationContext{
		Agents:        sel.spec.Delegates,
		ParentSession: meta.SessionID,
		Subject:       meta.UserID,
		SubjectKind:   meta.SubjectKind,
	}
	if meta.Lineage != nil {
		delegation.RootRunID = meta.Lineage.RootRunID
		delegation.Depth = meta.Lineage.Depth
	}
	return delegation
}

// delegateSession derives the delegate's conversation from the caller's, so one
// conversation delegates to the same agent in the same context across turns
// instead of starting from nothing each time.
func delegateSession(parent, agent string) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		// No parent conversation to hang the delegate's history on; a fresh one
		// is better than a shared global one, which would leak context between
		// callers.
		return "delegate-" + uuid.NewString()
	}
	return parent + "/delegate/" + agent
}

// collect drains a run's events into the tool result.
//
// A stream that ends without a terminal event is an error rather than an empty
// success: it means the run was cut off, and reporting "completed" with no
// answer would let the caller believe the work was done.
func collect(
	ctx context.Context, taskID, session string, events <-chan agentapi.Event,
) (capability.AgentResult, error) {
	result := capability.AgentResult{Session: session, TaskID: taskID, State: "completed"}
	var text strings.Builder
	terminal := false
	for event := range events {
		switch event.Type {
		case agentapi.EventAnswer:
			text.WriteString(event.Delta)
		case agentapi.EventDone:
			terminal = true
			if event.Final != "" {
				result.Text = event.Final
			} else {
				result.Text = text.String()
			}
		case agentapi.EventInterrupted:
			terminal = true
			result.State = "input-required"
			result.Interrupts = interruptsOf(event.Interrupt)
		case agentapi.EventFailed:
			terminal = true
			result.State = "failed"
			if event.Err != nil {
				result.Error = event.Err.Error()
			}
		case agentapi.EventStopped:
			terminal = true
			result.State = "canceled"
		}
	}
	if !terminal {
		if err := ctx.Err(); err != nil {
			return capability.AgentResult{}, fmt.Errorf("delegated run %s was cut off: %w", taskID, err)
		}
		return capability.AgentResult{}, fmt.Errorf("delegated run %s ended without a terminal event", taskID)
	}
	return result, nil
}

// interruptsOf flattens a pause into the pending requests a caller can answer.
func interruptsOf(interrupt *agentapi.Interrupt) []capability.AgentInterrupt {
	if interrupt == nil {
		return nil
	}
	out := make([]capability.AgentInterrupt, 0, len(interrupt.Requests))
	for _, request := range interrupt.Requests {
		out = append(out, capability.AgentInterrupt{
			RequestID: interrupt.RequestID,
			Kind:      request.Kind,
			ToolName:  request.ToolName,
			Question:  request.Question,
			Options:   request.Options,
		})
	}
	if len(out) == 0 {
		out = append(out, capability.AgentInterrupt{RequestID: interrupt.RequestID})
	}
	return out
}
