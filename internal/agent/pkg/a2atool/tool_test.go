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

package a2atool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/a2ainvoke"
)

type fakeAgents struct{ rows map[string]*model.AgentM }

func (f *fakeAgents) Create(context.Context, *model.AgentM) error  { return nil }
func (f *fakeAgents) Update(context.Context, *model.AgentM) error  { return nil }
func (f *fakeAgents) Delete(context.Context, *where.Options) error { return nil }
func (f *fakeAgents) Get(_ context.Context, opts *where.Options) (*model.AgentM, error) {
	name, _ := opts.Filters["name"].(string)
	return f.rows[name], nil
}
func (f *fakeAgents) List(context.Context, *where.Options) (int64, []*model.AgentM, error) {
	out := make([]*model.AgentM, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, row)
	}
	return int64(len(out)), out, nil
}
func (f *fakeAgents) A2ACredentials(context.Context, string) (map[string][]byte, error) {
	return nil, nil
}

type fakeInvocations struct{}

func (fakeInvocations) Create(context.Context, *model.A2AInvocationM) error { return nil }
func (fakeInvocations) Get(context.Context, *where.Options) (*model.A2AInvocationM, error) {
	return nil, nil
}
func (fakeInvocations) List(context.Context, *where.Options) (int64, []*model.A2AInvocationM, error) {
	return 0, nil, nil
}

func TestDispatcherTargetsOnlyExternalAgents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"kind":"task","id":"task-1","status":{"state":"completed"},
			"artifacts":[{"artifactId":"a1","parts":[{"kind":"text","text":"ok"}]}]
		}}`))
	}))
	defer server.Close()

	execution, _ := json.Marshal(model.AgentExecutionM{
		Mode: "a2a",
		A2A:  &model.A2AExecutionM{Source: "card-url", Endpoint: server.URL},
	})
	status, _ := json.Marshal(model.AgentA2AStatusM{
		ProtocolVersion: "0.3", Transport: "JSONRPC", ResolvedEndpoint: server.URL,
		Skills: []model.A2ASkillM{{ID: "echo", Name: "Echo"}},
	})
	agents := &fakeAgents{rows: map[string]*model.AgentM{
		"remote": {
			Name: "remote", Kind: "external",
			ExecutionJSON: string(execution), A2AStatusJSON: string(status),
		},
		"native": {Name: "native", Kind: "native"},
	}}
	service := a2ainvoke.New(agents, fakeInvocations{}, agents)
	provider := New(agents, service)

	tools := provider.Tools(context.Background(), []string{"remote", "native"})
	if len(tools) != 1 {
		t.Fatalf("tools = %d", len(tools))
	}
	info, err := tools[0].Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolName {
		t.Fatalf("name = %q", info.Name)
	}
	if !strings.Contains(info.Desc, "remote") || strings.Contains(info.Desc, "native") {
		t.Fatalf("description = %q", info.Desc)
	}

	invokable, ok := tools[0].(tool.InvokableTool)
	if !ok {
		t.Fatalf("tool does not implement InvokableTool: %T", tools[0])
	}
	out, err := invokable.InvokableRun(context.Background(), `{"agent":"remote","message":"hi"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var payload struct {
		State string `json:"state"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode result: %v (%s)", err, out)
	}
	if payload.State != "completed" || payload.Text != "ok" {
		t.Fatalf("payload = %+v", payload)
	}
}
