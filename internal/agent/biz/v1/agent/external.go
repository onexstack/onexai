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
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/a2ainvoke"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// askExternal routes a selection that names exactly one external A2A agent to
// the A2A client. It returns ok=false for every other selection, which is what
// leaves the ordinary engine path unchanged.
//
// The external agent has no platform prompt, model or tool set, so running it
// through the engine would answer with the deployment assistant instead of the
// agent the user selected. Routing here is what makes an external agent a
// first-class Chat target rather than only a tool.
//
// It reads the selection the admission gate already resolved rather than
// loading the agent a second time: one question about a selection has one
// answer, and a second read could disagree with the gate's about which agent
// the run is for.
func (b *AgentBiz) askExternal(
	ctx context.Context, req *agentapi.AskRequest, sel resolvedSelection, lease *runLease,
) (<-chan agentapi.Event, bool, error) {
	if b == nil || req == nil {
		return nil, false, nil
	}
	var row *model.AgentM
	for _, record := range sel.set.records {
		if record != nil && record.Kind == "external" {
			row = record
			break
		}
	}
	if row == nil {
		return nil, false, nil
	}
	if b.deps.A2A == nil {
		// Falling through to the engine here would answer with the deployment
		// assistant instead of the agent the user picked. The wiring always
		// provides this service; refusing names the actual fault.
		return nil, true, errno.ErrNotConfigured.WithMessage("A2A execution is not configured on this server")
	}
	name := row.Name
	// An external agent stands alone: it has no prompt or tools to combine with
	// another agent's, and composition replaces capability sets it never had.
	// Refusing is clearer than silently running only one half of the selection.
	if len(req.Selection.Agents) != 1 {
		return nil, true, errno.ErrInvalidArgument.WithMessage("external A2A agent %q cannot be combined with other agents", name)
	}
	if req.Composition != nil {
		return nil, true, errno.ErrInvalidArgument.WithMessage("external A2A agent %q does not support capability composition", name)
	}
	if row.Status == model.StatusDisabled {
		return nil, true, errno.ErrInvalidArgument.WithMessage("external A2A agent %q is disabled", name)
	}
	var execution model.AgentExecutionM
	if row.ExecutionJSON == "" || json.Unmarshal([]byte(row.ExecutionJSON), &execution) != nil ||
		execution.Mode != "a2a" || execution.A2A == nil {
		return nil, true, errno.ErrInvalidArgument.WithMessage("agent %q is not an A2A agent", name)
	}
	var status model.AgentA2AStatusM
	if row.A2AStatusJSON == "" || json.Unmarshal([]byte(row.A2AStatusJSON), &status) != nil ||
		status.CardDigest == "" {
		return nil, true, errno.ErrInvalidArgument.WithMessage("external agent %q has no synced AgentCard", name)
	}
	text := strings.TrimSpace(req.Input.Text)
	if text == "" {
		return nil, true, errno.ErrInvalidArgument.WithMessage("message is required")
	}

	// Stopping must cancel the outbound call, not the run. A hard cancel of the
	// run's context would reach the relay too, and the relay settles the run
	// (and writes its terminal event) on that context — a cancelled one leaves
	// the row running forever. The child context is the stop's target, exactly
	// like the engine's safe-point stop.
	callCtx, cancelCall := context.WithCancel(ctx)
	if lease != nil {
		lease.stopReady(req.RunID)(cancelCall)
	}

	out := make(chan agentapi.Event, 16)
	go func() {
		defer close(out)
		defer cancelCall()
		send := func(event agentapi.Event) bool {
			select {
			case out <- event:
				return true
			case <-callCtx.Done():
				return false
			}
		}
		// Stopping cancels the call context, so the ordinary send refuses. The
		// relay still needs the terminal event to record the turn as stopped
		// rather than as a stream that ended without an outcome. The send is
		// bounded rather than non-blocking: a full caller buffer is exactly the
		// state in which send() failed, and dropping the terminal event there
		// would lose the only signal that says why the stream ended.
		stopped := func() {
			select {
			case out <- agentapi.Event{Type: agentapi.EventStopped}:
			case <-time.After(2 * time.Second):
			}
		}

		_, events, err := b.deps.A2A.Stream(callCtx, a2ainvoke.CallRequest{
			Name:        name,
			Message:     text,
			ContextID:   req.SessionID,
			Source:      "chat",
			CallerKind:  string(req.Subject.Kind),
			CallerID:    req.Subject.ID,
			CallerName:  req.Subject.Name,
			ParentRunID: req.RunID,
			RequestID:   req.RunID,
			// The run's own deadline, when the surface set one. Zero leaves the
			// agent's execution.a2a.timeoutSeconds in charge.
			Timeout: req.Options.Deadline,
		})
		if err != nil {
			if callCtx.Err() != nil {
				stopped()
				return
			}
			_ = send(agentapi.Event{Type: agentapi.EventFailed, Err: err})
			return
		}

		var answer strings.Builder
		for event := range events {
			switch event.Type {
			case "delta":
				if event.Text != "" {
					answer.WriteString(event.Text)
					if !send(agentapi.Event{Type: agentapi.EventAnswer, Delta: event.Text}) {
						stopped()
						return
					}
				}
			case "artifact":
				if event.Artifact == nil {
					continue
				}
				for _, part := range event.Artifact.Parts {
					if part.Kind != "text" || part.Text == "" {
						continue
					}
					answer.WriteString(part.Text)
					if !send(agentapi.Event{Type: agentapi.EventAnswer, Delta: part.Text}) {
						stopped()
						return
					}
				}
			case "interrupt":
				// The client normalizes input-required / auth-required into its
				// own type, so this is the shape those pauses actually arrive
				// with. Chat has no engine checkpoint to resume the remote task
				// against; surface the question as the turn's answer and let the
				// detail page's debug view continue it.
				message := event.Message
				if message == "" {
					message = "the remote A2A agent needs more input"
				}
				answer.WriteString(message)
				if !send(agentapi.Event{Type: agentapi.EventAnswer, Delta: message}) {
					stopped()
					return
				}
				_ = send(agentapi.Event{Type: agentapi.EventDone, Final: answer.String()})
				return
			case "status":
				switch event.State {
				case "completed":
					// A peer may deliver the answer only with its terminal
					// event: no deltas, no artifact-update events, just one
					// task snapshot. The service accumulates that text onto the
					// final event, so use it when nothing streamed before.
					if answer.Len() == 0 && event.Text != "" {
						answer.WriteString(event.Text)
						if !send(agentapi.Event{Type: agentapi.EventAnswer, Delta: event.Text}) {
							return
						}
					}
					_ = send(agentapi.Event{Type: agentapi.EventDone, Final: answer.String()})
					return
				case "failed", "rejected":
					_ = send(agentapi.Event{Type: agentapi.EventFailed, Err: errors.New(event.Message)})
					return
				case "canceled":
					// A terminal state the remote chose, not one the caller did.
					// Recording it as success would be wrong; recording it as a
					// stream without an outcome hides the reason.
					if !send(agentapi.Event{Type: agentapi.EventStopped}) {
						stopped()
					}
					return
				case "input-required", "auth-required":
					// Chat has no engine checkpoint to resume this task against;
					// surface the remote question as the turn's answer. The A2A
					// detail page is the place that can continue the task.
					message := event.Message
					if message == "" {
						message = "the remote A2A agent needs more input"
					}
					answer.WriteString(message)
					_ = send(agentapi.Event{Type: agentapi.EventAnswer, Delta: message})
					_ = send(agentapi.Event{Type: agentapi.EventDone, Final: answer.String()})
					return
				}
			case "error":
				_ = send(agentapi.Event{Type: agentapi.EventFailed, Err: errors.New(event.Message)})
				return
			}
		}
		if callCtx.Err() != nil {
			stopped()
			return
		}
		if answer.Len() > 0 {
			_ = send(agentapi.Event{Type: agentapi.EventDone, Final: answer.String()})
			return
		}
		_ = send(agentapi.Event{Type: agentapi.EventFailed, Err: errors.New("A2A stream ended without a terminal event")})
	}()
	return out, true, nil
}
