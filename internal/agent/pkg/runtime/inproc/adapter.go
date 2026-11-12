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

// Package inproc adapts the in-process eino engine to the runtime port. It is
// intentionally thin: it translates the A2A-shaped request into the values the
// engine already takes, and leaves every eino concern (tools, middleware,
// approvals, compaction) untouched.
package inproc

import (
	"context"
	"errors"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"

	agent2a "github.com/onexstack/onexai/internal/agent/pkg/a2a"
	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// ErrNotPending is returned when a resume names a run with no recorded pause.
var ErrNotPending = errors.New("the run is not waiting for an answer")

// Adapter is the in-process eino runtime.
type Adapter struct {
	Engine      agentapi.Engine
	Resumable   agentapi.Resumable
	Sessions    mem.SessionStore
	Checkpoints checkpoint.Store
	Stops       runstop.Registry

	// NameValue is the port name (defaults to "eino").
	NameValue string
}

var _ runtime.Port = (*Adapter)(nil)

// Name implements runtime.Port.
func (a *Adapter) Name() string {
	if a.NameValue != "" {
		return a.NameValue
	}
	return "eino"
}

// Kind implements runtime.Port.
func (a *Adapter) Kind() string { return "inproc" }

// Card implements runtime.Port. The card is the capability negotiation input;
// it advertises what this adapter actually emits.
func (a *Adapter) Card(context.Context) (*a2a.AgentCard, error) {
	return &a2a.AgentCard{
		Name:        a.Name(),
		Description: "onexai in-process eino runtime",
		Version:     "v1",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface("inproc://eino", a2a.TransportProtocolJSONRPC),
		},
		Capabilities: a2a.AgentCapabilities{
			Streaming: true,
			Extensions: []a2a.AgentExtension{
				agent2a.Extension(agent2a.HITLExtensionURI, "Human approval and user input", nil),
				agent2a.Extension(agent2a.UsageExtensionURI, "Task-lifetime token usage", nil),
				agent2a.Extension(agent2a.StructuredOutputExtensionURI, "Schema-validated final answer", nil),
			},
		},
		DefaultInputModes:  []string{"text/plain", "image/png", "image/jpeg", "image/webp"},
		DefaultOutputModes: []string{"text/plain", "application/json"},
		Skills:             []a2a.AgentSkill{},
	}, nil
}

// Run implements runtime.Port.
func (a *Adapter) Run(ctx context.Context, req *runtime.Request) (<-chan agentapi.Event, error) {
	if req == nil || req.A2A == nil {
		return nil, fmt.Errorf("inproc: nil request")
	}
	if response, ok := runtime.HITLResponseFrom(req.A2A); ok {
		return a.resume(ctx, req, response)
	}

	messages := req.History
	if len(messages) == 0 && a.Sessions != nil && req.SessionKey != "" {
		history, err := a.Sessions.Load(ctx, req.SessionKey)
		if err != nil {
			return nil, fmt.Errorf("inproc: load session %q: %w", req.SessionKey, err)
		}
		messages = history
	}
	messages = append(append([]agentapi.Message{}, messages...), agentapi.Message{
		Role:    agentapi.RoleUser,
		Content: runtime.UserText(req.A2A),
	})

	events, err := a.Engine.Run(ctx, messages, req.Options)
	if err != nil {
		return nil, err
	}
	return events, nil
}

// resume continues the paused run named by req.RunID.
func (a *Adapter) resume(
	ctx context.Context, req *runtime.Request, response runtime.HITLResponse,
) (<-chan agentapi.Event, error) {
	if a.Resumable == nil {
		return nil, fmt.Errorf("inproc: the configured engine does not support resume")
	}
	if a.Checkpoints == nil {
		return nil, fmt.Errorf("inproc: resume needs a checkpoint store")
	}
	meta, found, err := a.Checkpoints.GetMeta(ctx, req.RunID)
	if err != nil {
		return nil, fmt.Errorf("inproc: load suspended run %q: %w", req.RunID, err)
	}
	if !found {
		return nil, ErrNotPending
	}
	if response.RequestID == "" || response.RequestID != meta.RequestID {
		return nil, ErrNotPending
	}

	opts := req.Options
	allowAll := meta.AllowAll || response.AllowAll || response.Decision == agentapi.DecisionAllowAll

	events, err := a.Resumable.Resume(ctx, agentapi.ResumeOptions{
		Run:       opts,
		RequestID: response.RequestID,
		Decision:  response.Decision,
		Answers:   response.Answers,
		Reason:    response.Reason,
		Pending:   meta.Pending,
		AllowAll:  allowAll,
	})
	if err != nil {
		return nil, fmt.Errorf("inproc: resume run %q: %w", req.RunID, err)
	}
	return events, nil
}

// Cancel implements runtime.Port.
func (a *Adapter) Cancel(ctx context.Context, runID string) error {
	if a.Stops == nil || runID == "" {
		return nil
	}
	return a.Stops.RequestStop(ctx, runID)
}
