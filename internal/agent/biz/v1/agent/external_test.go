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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/a2ainvoke"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// externalTestAgents is the one read a2ainvoke makes, plus the credential read
// it makes before a call. The rows are the projection the control plane hands
// the runtime.
type externalTestAgents struct{ rows map[string]*model.AgentM }

func (f *externalTestAgents) Create(context.Context, *model.AgentM) error  { return nil }
func (f *externalTestAgents) Update(context.Context, *model.AgentM) error  { return nil }
func (f *externalTestAgents) Delete(context.Context, *where.Options) error { return nil }
func (f *externalTestAgents) Get(_ context.Context, opts *where.Options) (*model.AgentM, error) {
	name, _ := opts.Filters["name"].(string)
	return f.rows[name], nil
}
func (f *externalTestAgents) List(context.Context, *where.Options) (int64, []*model.AgentM, error) {
	return 0, nil, nil
}
func (f *externalTestAgents) A2ACredentials(context.Context, string) (map[string][]byte, error) {
	return nil, nil
}

type externalTestInvocations struct{}

func (externalTestInvocations) Create(context.Context, *model.A2AInvocationM) error { return nil }
func (externalTestInvocations) Get(context.Context, *where.Options) (*model.A2AInvocationM, error) {
	return nil, nil
}
func (externalTestInvocations) List(
	context.Context, *where.Options,
) (int64, []*model.A2AInvocationM, error) {
	return 0, nil, nil
}

// externalRow builds the row the resolver would have loaded for a registered
// external agent. endpoint empty means no card has been synced.
func externalRow(name, endpoint, phase string) *model.AgentM {
	execution, _ := json.Marshal(model.AgentExecutionM{
		Mode: "a2a",
		A2A:  &model.A2AExecutionM{Source: "card-url", Endpoint: endpoint},
	})
	status, _ := json.Marshal(model.AgentA2AStatusM{
		Phase: phase, ProtocolVersion: "0.3", Transport: "JSONRPC",
		ResolvedEndpoint: endpoint, CardDigest: "sha256:card",
	})
	if phase == "" {
		status = []byte(`{}`)
	}
	return &model.AgentM{
		Name: name, Kind: "external", Status: model.StatusEnabled,
		ExecutionJSON: string(execution), A2AStatusJSON: string(status),
	}
}

func selectionOf(rows ...*model.AgentM) resolvedSelection {
	sel := resolvedSelection{}
	for _, row := range rows {
		sel.set.names = append(sel.set.names, row.Name)
		sel.set.records = append(sel.set.records, row)
	}
	return sel
}

func externalBiz(t *testing.T, rows map[string]*model.AgentM) *AgentBiz {
	t.Helper()
	agents := &externalTestAgents{rows: rows}
	return New(Deps{A2A: a2ainvoke.New(agents, externalTestInvocations{}, agents)})
}

func askRequest(name string) *agentapi.AskRequest {
	return &agentapi.AskRequest{
		RunID:     "run-1",
		SessionID: "sess-1",
		Input:     agentapi.Input{Text: "hello"},
		Selection: agentapi.Selection{Agents: []string{name}},
	}
}

// TestAskExternalIgnoresNativeSelection pins that the ordinary engine path is
// untouched: a native agent must never be routed to the A2A client.
func TestAskExternalIgnoresNativeSelection(t *testing.T) {
	b := externalBiz(t, map[string]*model.AgentM{"alpha": {Name: "alpha", Kind: "native"}})

	ch, ok, err := b.askExternal(context.Background(), askRequest("alpha"), selectionOf(&model.AgentM{Name: "alpha", Kind: "native"}), nil)
	if err != nil || ok || ch != nil {
		t.Fatalf("native selection routed: ok=%v err=%v ch=%v", ok, err, ch)
	}
}

// TestAskExternalRequiresSyncedCard refuses rather than falling back to the
// engine: an external agent the controller has not read a card for has no
// endpoint, and answering with the deployment assistant would be worse than an
// error that names the cause.
func TestAskExternalRequiresSyncedCard(t *testing.T) {
	b := externalBiz(t, map[string]*model.AgentM{"remote": externalRow("remote", "", "")})

	_, ok, err := b.askExternal(context.Background(), askRequest("remote"), selectionOf(externalRow("remote", "", "")), nil)
	if !ok || err == nil || !strings.Contains(err.Error(), "AgentCard") {
		t.Fatalf("ok=%v err=%v, want a card error", ok, err)
	}
}

// TestAskExternalRefusesCompositionAndMix pins the two shapes the remote cannot
// represent: composition replaces capabilities an external agent never had, and
// a multi-agent selection has no single peer to call.
func TestAskExternalRefusesCompositionAndMix(t *testing.T) {
	remote := externalRow("remote", "http://127.0.0.1:1", "Ready")
	b := externalBiz(t, map[string]*model.AgentM{"remote": remote})

	composed := askRequest("remote")
	composed.Composition = &agentapi.AgentComposition{Prompts: []string{"x"}}
	if _, ok, err := b.askExternal(context.Background(), composed, selectionOf(remote), nil); !ok || err == nil {
		t.Fatalf("composition: ok=%v err=%v, want a refusal", ok, err)
	}

	mixed := askRequest("remote")
	mixed.Selection.Agents = []string{"remote", "alpha"}
	native := &model.AgentM{Name: "alpha", Kind: "native"}
	if _, ok, err := b.askExternal(context.Background(), mixed, selectionOf(remote, native), nil); !ok || err == nil {
		t.Fatalf("mixed: ok=%v err=%v, want a refusal", ok, err)
	}
}

// TestAskExternalStreamsTheAnswer checks the bridge: artifact events become
// answer deltas and the terminal status becomes EventDone carrying the full
// text, which is what the relay persists as the assistant turn.
func TestAskExternalStreamsTheAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","taskId":"t1","status":{"state":"working"},"final":false}}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"artifact-update","taskId":"t1","artifact":{"artifactId":"a1","parts":[{"kind":"text","text":"mock reply"}]}}}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","taskId":"t1","status":{"state":"completed"},"final":true}}` + "\n\n"))
	}))
	defer server.Close()

	remote := externalRow("remote", server.URL, "Ready")
	b := externalBiz(t, map[string]*model.AgentM{"remote": remote})

	ch, ok, err := b.askExternal(context.Background(), askRequest("remote"), selectionOf(remote), nil)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	events := drain(t, ch)

	var answer, final string
	for _, event := range events {
		switch event.Type {
		case agentapi.EventAnswer:
			answer += event.Delta
		case agentapi.EventDone:
			final = event.Final
		case agentapi.EventFailed:
			t.Fatalf("run failed: %v", event.Err)
		}
	}
	if answer != "mock reply" || final != "mock reply" {
		t.Fatalf("answer=%q final=%q, want both %q", answer, final, "mock reply")
	}
}

// TestAskExternalUsesTerminalTaskText covers the peers that answer a stream
// with one whole task instead of deltas. Without lifting that text onto the
// terminal event the turn would finish with an empty answer.
func TestAskExternalUsesTerminalTaskText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"task","id":"t1","contextId":"c1","status":{"state":"completed"},"artifacts":[{"artifactId":"a1","parts":[{"kind":"text","text":"whole task"}]}]}}` + "\n\n"))
	}))
	defer server.Close()

	remote := externalRow("remote", server.URL, "Ready")
	b := externalBiz(t, map[string]*model.AgentM{"remote": remote})

	ch, ok, err := b.askExternal(context.Background(), askRequest("remote"), selectionOf(remote), nil)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var answer, final string
	for _, event := range drain(t, ch) {
		switch event.Type {
		case agentapi.EventAnswer:
			answer += event.Delta
		case agentapi.EventDone:
			final = event.Final
		case agentapi.EventFailed:
			t.Fatalf("run failed: %v", event.Err)
		}
	}
	if answer != "whole task" || final != "whole task" {
		t.Fatalf("answer=%q final=%q, want both %q", answer, final, "whole task")
	}
}

// TestAskExternalSurfacesInputRequired pins the pause shape. The client
// normalizes input-required into an "interrupt" event, not a status event with
// that state; a bridge that only matched "status" would end the turn as a
// stream that never reported an outcome.
func TestAskExternalSurfacesInputRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","taskId":"t1","contextId":"c1","status":{"state":"input-required","message":{"role":"agent","parts":[{"kind":"text","text":"what is your name?"}]}},"final":false}}` + "\n\n"))
	}))
	defer server.Close()

	remote := externalRow("remote", server.URL, "Ready")
	b := externalBiz(t, map[string]*model.AgentM{"remote": remote})

	ch, ok, err := b.askExternal(context.Background(), askRequest("remote"), selectionOf(remote), nil)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var answer, final string
	for _, event := range drain(t, ch) {
		switch event.Type {
		case agentapi.EventAnswer:
			answer += event.Delta
		case agentapi.EventDone:
			final = event.Final
		case agentapi.EventFailed:
			t.Fatalf("run failed: %v", event.Err)
		}
	}
	if answer != "what is your name?" || final != "what is your name?" {
		t.Fatalf("answer=%q final=%q, want the remote question", answer, final)
	}
}

// TestAskExternalStopKeepsTheRunContextAlive pins the stop contract. A stop
// must cancel the outbound call only: if it cancelled the run's context, the
// relay would settle the run on a cancelled context, the run row would stay
// running forever, and the terminal event would never be recorded.
func TestAskExternalStopKeepsTheRunContextAlive(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","taskId":"t1","status":{"state":"working"},"final":false}}` + "\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	remote := externalRow("remote", server.URL, "Ready")
	b := externalBiz(t, map[string]*model.AgentM{"remote": remote})
	b.deps.Stops = runstop.NewLocal()

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	lease := &runLease{b: b}

	ch, ok, err := b.askExternal(runCtx, askRequest("remote"), selectionOf(remote), lease)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the outbound stream never started")
	}
	b.deps.Stops.StopLocal("run-1")

	events := drain(t, ch)
	if len(events) == 0 || events[len(events)-1].Type != agentapi.EventStopped {
		t.Fatalf("events = %+v, want a terminal stopped event", events)
	}
	if runCtx.Err() != nil {
		t.Fatalf("stop cancelled the run context: %v", runCtx.Err())
	}
}

// TestAskExternalMapsRemoteCancelToStopped pins the last terminal state. A
// canceled task that arrived after some streamed text used to be published as
// EventDone — a success the peer never claimed.
func TestAskExternalMapsRemoteCancelToStopped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"artifact-update","taskId":"t1","artifact":{"artifactId":"a1","parts":[{"kind":"text","text":"partial"}]}}}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","taskId":"t1","status":{"state":"canceled"},"final":true}}` + "\n\n"))
	}))
	defer server.Close()

	remote := externalRow("remote", server.URL, "Ready")
	b := externalBiz(t, map[string]*model.AgentM{"remote": remote})

	ch, ok, err := b.askExternal(context.Background(), askRequest("remote"), selectionOf(remote), nil)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	events := drain(t, ch)
	if len(events) == 0 {
		t.Fatal("no events")
	}
	last := events[len(events)-1]
	if last.Type != agentapi.EventStopped {
		t.Fatalf("terminal event = %v, want stopped", last.Type)
	}
}
