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

package a2ainvoke

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/a2aclient"
)

type fakeAgents struct{ row *model.AgentM }

func (f *fakeAgents) Create(context.Context, *model.AgentM) error  { return nil }
func (f *fakeAgents) Update(context.Context, *model.AgentM) error  { return nil }
func (f *fakeAgents) Delete(context.Context, *where.Options) error { return nil }
func (f *fakeAgents) Get(context.Context, *where.Options) (*model.AgentM, error) {
	return f.row, nil
}
func (f *fakeAgents) List(context.Context, *where.Options) (int64, []*model.AgentM, error) {
	return 1, []*model.AgentM{f.row}, nil
}
func (f *fakeAgents) A2ACredentials(context.Context, string) (map[string][]byte, error) {
	return map[string][]byte{"apiKey": []byte("secret")}, nil
}

type fakeInvocations struct{ rows []*model.A2AInvocationM }

func (f *fakeInvocations) Create(_ context.Context, row *model.A2AInvocationM) error {
	f.rows = append(f.rows, row)
	return nil
}
func (f *fakeInvocations) Get(context.Context, *where.Options) (*model.A2AInvocationM, error) {
	if len(f.rows) == 0 {
		return nil, nil
	}
	return f.rows[0], nil
}
func (f *fakeInvocations) List(context.Context, *where.Options) (int64, []*model.A2AInvocationM, error) {
	return int64(len(f.rows)), f.rows, nil
}

func TestInvokeBuildsTheWireClientAndRecordsIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-KEY"); got != "secret" {
			t.Errorf("api key header = %q", got)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"kind":"task","id":"task-1","contextId":"ctx-1",
			"status":{"state":"completed"},
			"artifacts":[{"artifactId":"a1","parts":[{"kind":"text","text":"ok"}]}]
		}}`))
	}))
	defer server.Close()

	execution, _ := json.Marshal(model.AgentExecutionM{
		Mode: "a2a",
		A2A: &model.A2AExecutionM{
			Source: "card-url", Endpoint: server.URL,
			Auth: &model.A2AAuthM{Type: "apiKey", APIKeyHeader: "X-API-KEY"},
		},
	})
	status, _ := json.Marshal(model.AgentA2AStatusM{
		ProtocolVersion:  a2aclient.ProtocolVersion03,
		Transport:        a2aclient.TransportJSONRPC,
		ResolvedEndpoint: server.URL,
	})
	agents := &fakeAgents{row: &model.AgentM{
		Name: "remote", Namespace: "default", Kind: "external",
		ExecutionJSON: string(execution), A2AStatusJSON: string(status),
	}}
	records := &fakeInvocations{}
	service := New(agents, records, agents)

	result, err := service.Invoke(context.Background(), CallRequest{
		Name: "remote", Message: "hi", Source: "console",
		CallerKind: "user", CallerID: "u1", CallerName: "Colin",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.Result == nil || result.Result.Text != "ok" || result.Result.TaskID != "task-1" {
		t.Fatalf("result = %+v", result)
	}
	if len(records.rows) != 1 {
		t.Fatalf("records = %d", len(records.rows))
	}
	record := records.rows[0]
	if record.State != a2aclient.StateCompleted || record.TaskID != "task-1" ||
		record.AgentName != "remote" || record.CallerID != "u1" {
		t.Fatalf("record = %+v", record)
	}
	if record.Output != "ok" {
		t.Fatalf("output = %q", record.Output)
	}
}

// TestCallTimeoutPrecedence pins which deadline a call actually uses. The
// request wins, then the agent's configured execution.a2a.timeoutSeconds, then
// the service default: the debug panel, the tool and Chat all enter through
// this one function, and only one of them used to send a value at all.
func TestCallTimeoutPrecedence(t *testing.T) {
	execution, _ := json.Marshal(model.AgentExecutionM{
		Mode: "a2a",
		A2A:  &model.A2AExecutionM{TimeoutSeconds: 42},
	})
	agent := &model.AgentM{Name: "remote", ExecutionJSON: string(execution)}

	svc := &Service{Timeout: 90 * time.Second}
	if got := svc.callTimeout(CallRequest{Timeout: 7 * time.Second}, agent); got != 7*time.Second {
		t.Fatalf("explicit timeout = %v", got)
	}
	if got := svc.callTimeout(CallRequest{}, agent); got != 42*time.Second {
		t.Fatalf("agent timeout = %v", got)
	}
	if got := svc.callTimeout(CallRequest{}, &model.AgentM{Name: "remote"}); got != 90*time.Second {
		t.Fatalf("service timeout = %v", got)
	}
	if got := (&Service{}).callTimeout(CallRequest{}, nil); got != defaultTimeout {
		t.Fatalf("default timeout = %v", got)
	}
}

// TestStreamRecordsTheInterruptQuestion pins what a paused call leaves behind.
// The question travels on the interrupt event, not in any answer text, so a
// record that only read Text showed an empty output for exactly the calls a
// person needs to inspect before continuing.
func TestStreamRecordsTheInterruptQuestion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","taskId":"t1","contextId":"c1","status":{"state":"input-required","message":{"role":"agent","parts":[{"kind":"text","text":"what is your name?"}]}},"final":false}}` + "\n\n"))
	}))
	defer server.Close()

	execution, _ := json.Marshal(model.AgentExecutionM{
		Mode: "a2a",
		A2A:  &model.A2AExecutionM{Source: "card-url", Endpoint: server.URL},
	})
	status, _ := json.Marshal(model.AgentA2AStatusM{
		ProtocolVersion:  a2aclient.ProtocolVersion03,
		Transport:        a2aclient.TransportJSONRPC,
		ResolvedEndpoint: server.URL,
	})
	agents := &fakeAgents{row: &model.AgentM{
		Name: "remote", Namespace: "default", Kind: "external",
		ExecutionJSON: string(execution), A2AStatusJSON: string(status),
	}}
	records := &fakeInvocations{}
	service := New(agents, records, agents)

	_, events, err := service.Stream(context.Background(), CallRequest{
		Name: "remote", Message: "hi", Source: "chat",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range events {
	}
	if len(records.rows) != 1 {
		t.Fatalf("records = %d", len(records.rows))
	}
	record := records.rows[0]
	if record.State != a2aclient.StateInputRequired {
		t.Fatalf("state = %q", record.State)
	}
	if record.Output != "what is your name?" {
		t.Fatalf("output = %q, want the interrupt question", record.Output)
	}
}

// TestStreamRecordsTaskSnapshotAnswer covers the peers that answer a stream with
// one whole task. Its text and artifacts live on the terminal event, so a
// record that only read deltas would show a successful call with no output.
func TestStreamRecordsTaskSnapshotAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"jsonrpc":"2.0","id":1,"result":{"kind":"task","id":"t1","contextId":"c1","status":{"state":"completed"},"artifacts":[{"artifactId":"a1","parts":[{"kind":"text","text":"snapshot answer"}]}]}}` + "\n\n"))
	}))
	defer server.Close()

	execution, _ := json.Marshal(model.AgentExecutionM{
		Mode: "a2a",
		A2A:  &model.A2AExecutionM{Source: "card-url", Endpoint: server.URL},
	})
	status, _ := json.Marshal(model.AgentA2AStatusM{
		ProtocolVersion:  a2aclient.ProtocolVersion03,
		Transport:        a2aclient.TransportJSONRPC,
		ResolvedEndpoint: server.URL,
	})
	agents := &fakeAgents{row: &model.AgentM{
		Name: "remote", Namespace: "default", Kind: "external",
		ExecutionJSON: string(execution), A2AStatusJSON: string(status),
	}}
	records := &fakeInvocations{}
	service := New(agents, records, agents)

	_, events, err := service.Stream(context.Background(), CallRequest{
		Name: "remote", Message: "hi", Source: "chat",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range events {
	}
	if len(records.rows) != 1 {
		t.Fatalf("records = %d", len(records.rows))
	}
	record := records.rows[0]
	if record.State != a2aclient.StateCompleted {
		t.Fatalf("state = %q", record.State)
	}
	if record.Output != "snapshot answer" {
		t.Fatalf("output = %q", record.Output)
	}
	if record.ArtifactCount != 1 {
		t.Fatalf("artifact count = %d", record.ArtifactCount)
	}
}

// TestStreamRecordsCanceledWhenStopped pins that a stop before the peer reports
// any state is still a canceled call, not an empty row that reads as "never
// ran".
func TestStreamRecordsCanceledWhenStopped(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	execution, _ := json.Marshal(model.AgentExecutionM{
		Mode: "a2a",
		A2A:  &model.A2AExecutionM{Source: "card-url", Endpoint: server.URL},
	})
	status, _ := json.Marshal(model.AgentA2AStatusM{
		ProtocolVersion:  a2aclient.ProtocolVersion03,
		Transport:        a2aclient.TransportJSONRPC,
		ResolvedEndpoint: server.URL,
	})
	agents := &fakeAgents{row: &model.AgentM{
		Name: "remote", Namespace: "default", Kind: "external",
		ExecutionJSON: string(execution), A2AStatusJSON: string(status),
	}}
	records := &fakeInvocations{}
	service := New(agents, records, agents)

	ctx, cancel := context.WithCancel(context.Background())
	_, events, err := service.Stream(ctx, CallRequest{Name: "remote", Message: "hi", Source: "chat"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream never started")
	}
	cancel()
	for range events {
	}
	if len(records.rows) != 1 {
		t.Fatalf("records = %d", len(records.rows))
	}
	if records.rows[0].State != a2aclient.StateCanceled {
		t.Fatalf("state = %q, want canceled", records.rows[0].State)
	}
}
