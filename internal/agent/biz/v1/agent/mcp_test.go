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
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	"github.com/onexstack/onexai/internal/agent/store"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// captureEngine records the RunOptions and the messages of the runs it serves.
// Run is called synchronously from Ask, so the message field needs no locking.
type captureEngine struct {
	opts chan agentapi.RunOptions
	msgs []agentapi.Message
}

func (e *captureEngine) Name() string { return "capture" }

func (e *captureEngine) Run(_ context.Context, msgs []agentapi.Message, opts agentapi.RunOptions) (<-chan agentapi.Event, error) {
	e.msgs = append([]agentapi.Message(nil), msgs...)
	e.opts <- opts
	out := make(chan agentapi.Event, 1)
	go func() {
		defer close(out)
		out <- agentapi.Event{Type: agentapi.EventDone, Final: "ok"}
	}()
	return out, nil
}

// mcpResources serves agents whose MCP selections exercise the union rules.
func mcpResources() *fakeAIStore {
	return &fakeAIStore{
		AIStore: store.NewEmptyStore(),
		agents: map[string]*model.AgentM{
			"sre":   {Name: "sre", MCPs: []string{"zke", "grafana"}},
			"k8s":   {Name: "k8s", MCPs: []string{"grafana", "zke"}},
			"plain": {Name: "plain"},
			"blank": {Name: "blank", MCPs: []string{"   ", "zke"}},
		},
	}
}

func TestResolveMCPs(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		agents []string
		want   []string
	}{
		{"single agent", []string{"sre"}, []string{"zke", "grafana"}},
		{"union dedupes and keeps first-seen order", []string{"sre", "k8s"}, []string{"zke", "grafana"}},
		{"selection order does not matter", []string{"k8s", "sre"}, []string{"grafana", "zke"}},
		{"agent without mcps", []string{"plain"}, nil},
		{"unknown agent is skipped", []string{"nope", "plain"}, nil},
		{"blank names are dropped", []string{"blank"}, []string{"zke"}},
		{"whitespace-only names ignored", []string{"  "}, nil},
		{"no agents selected", nil, nil},
	}

	res := mcpResources()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewResolver(res, nil).Resolve(ctx, tt.agents).MCPs
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("resolveMCPs(%q) = %q, want %q", tt.agents, got, tt.want)
			}
		})
	}

	t.Run("nil resources yields nothing", func(t *testing.T) {
		if got := NewResolver(nil, nil).Resolve(ctx, []string{"sre"}).MCPs; got != nil {
			t.Fatalf("got %q, want nil", got)
		}
	})
}

func TestAgentBiz_PassesMCPsToEngine(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		agents []string
		want   []string
	}{
		{"selected agents contribute their servers", []string{"sre", "k8s"}, []string{"zke", "grafana"}},
		// The load-bearing case: a run with no agent, or an agent that declares
		// no MCP servers, must receive an empty selection rather than every
		// server in the store.
		{"no agent selected means no MCP tools", nil, nil},
		{"agent without mcps means no MCP tools", []string{"plain"}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &captureEngine{opts: make(chan agentapi.RunOptions, 1)}
			b := New(Deps{
				Engine:               engine,
				Sessions:             mem.NewSessionStore(nil, "test", 0),
				Stops:                runstop.NewLocal(),
				MaxConcurrentPerConv: 1,
				Resources:            mcpResources(),
			})

			ch, err := b.Ask(ctx, &agentapi.AskRequest{SessionID: "c1", Input: agentapi.Input{Text: "hi"}, Selection: agentapi.Selection{Agents: tt.agents}})
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}
			drain(t, ch)

			select {
			case opts := <-engine.opts:
				if fmt.Sprint(opts.MCPs) != fmt.Sprint(tt.want) {
					t.Fatalf("RunOptions.MCPs = %q, want %q", opts.MCPs, tt.want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("the engine was never run")
			}
		})
	}
}
