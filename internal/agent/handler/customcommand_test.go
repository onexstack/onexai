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

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/biz"
	customcommandbiz "github.com/onexstack/onexai/internal/agent/biz/v1/customcommand"
	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// memCommandStore is an in-memory CustomCommandStore for the HTTP-level test.
type memCommandStore struct {
	mu   sync.Mutex
	rows map[string]*model.CustomCommandM
}

func newMemCommandStore() *memCommandStore {
	return &memCommandStore{rows: map[string]*model.CustomCommandM{}}
}

func (m *memCommandStore) List(context.Context, *where.Options) (int64, []*model.CustomCommandM, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.CustomCommandM, 0, len(m.rows))
	for _, row := range m.rows {
		out = append(out, ptr.Clone(row))
	}
	return int64(len(out)), out, nil
}

func (m *memCommandStore) Get(_ context.Context, opts *where.Options) (*model.CustomCommandM, error) {
	name, _ := opts.Filters["name"].(string)
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[name]
	if !ok {
		return nil, agentstore.ErrNotFound
	}
	return ptr.Clone(row), nil
}

func (m *memCommandStore) Create(_ context.Context, row *model.CustomCommandM) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[row.Name] = ptr.Clone(row)
	return nil
}

func (m *memCommandStore) Update(_ context.Context, row *model.CustomCommandM) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[row.Name] = ptr.Clone(row)
	return nil
}

func (m *memCommandStore) Delete(_ context.Context, opts *where.Options) error {
	name, _ := opts.Filters["name"].(string)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, name)
	return nil
}

// customCommandResources exposes the in-memory command store; the other
// accessors are not reached by these routes.
type customCommandResources struct {
	agentstore.AIStore
	commands *memCommandStore
}

func (r customCommandResources) CustomCommands() agentstore.CustomCommandStore { return r.commands }

// fakeCustomCommands answers the invoke/context routes without an agent service.
// fakeCustomCommands answers the action endpoints. The CRUD methods come from
// the embedded (nil) interface: the CRUD tests drive the real biz over an
// in-memory store.
type fakeCustomCommands struct {
	customcommandbiz.CustomCommandBiz
}

func (fakeCustomCommands) Invoke(
	_ context.Context, name string, _ *v1.InvokeCustomCommandRequest,
) (*v1.InvokeCustomCommandResponse, error) {
	return &v1.InvokeCustomCommandResponse{
		Name: name,
		Kind: "prompt",
		Status: &v1.InvocationStatus{
			Phase:  "Succeeded",
			Prompt: &v1.PromptInvocation{Prompt: "rendered"},
		},
	}, nil
}

func (fakeCustomCommands) ContextCommands(
	_ context.Context, agent string,
) ([]*v1.ContextCustomCommand, error) {
	return []*v1.ContextCustomCommand{{Name: "hello", Type: "prompt", Usage: "<name>"}}, nil
}

func (fakeCustomCommands) ListAudit(
	_ context.Context, _ customcommandbiz.AuditQuery,
) (int64, []*v1.CustomCommandAudit, error) {
	return 1, []*v1.CustomCommandAudit{{Uid: "u1", Command: "hello", Phase: "Succeeded"}}, nil
}

func (fakeCustomCommands) Approve(
	_ context.Context, name string, req *v1.ApproveCustomCommandRequest,
) (*v1.ApproveCustomCommandResponse, error) {
	return &v1.ApproveCustomCommandResponse{
		ApprovalId: req.GetApprovalId(),
		Kind:       "tool",
		Name:       name,
		Status:     &v1.InvocationStatus{Phase: "Succeeded"},
	}, nil
}

func newCustomCommandTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	resources := customCommandResources{commands: newMemCommandStore()}
	// CRUD runs against the real implementation over the in-memory store; the
	// four action endpoints keep the fake so the test does not need a model,
	// an MCP manager or an approval store.
	commands := fakeCustomCommands{CustomCommandBiz: customcommandbiz.New(customcommandbiz.Deps{Resources: resources})}
	h := NewHandler(Deps{
		Biz:       biz.NewBiz(nil, nil, nil, commands, resources, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
		Resources: resources,
	})
	engine := gin.New()
	h.ApplyTo(engine.Group("/v1"))
	h.ApplyUnifiedTo(engine.Group("/v1"))
	return engine
}

func do(t *testing.T, engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestCustomCommandCRUDRoundTrip(t *testing.T) {
	engine := newCustomCommandTestEngine(t)

	create := `{"customCommand":{"name":"hello","displayName":"打招呼","enabled":true,"type":"prompt",
		"usage":"<name>","parameters":[{"name":"name","in":"positional","type":"string","required":true}],
		"prompt":{"template":"hi {{ .Params.name }}"}}}`
	rec := do(t, engine, http.MethodPost, "/v1/agent/customcommands", create)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created v1.CreateCustomCommandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.GetCustomCommand().GetType() != "prompt" {
		t.Errorf("type = %q", created.GetCustomCommand().GetType())
	}
	if len(created.GetCustomCommand().GetParameters()) != 1 {
		t.Errorf("parameters = %v", created.GetCustomCommand().GetParameters())
	}

	rec = do(t, engine, http.MethodGet, "/v1/agent/customcommands/hello", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rec.Code, rec.Body.String())
	}

	update := `{"customCommand":{"name":"hello","enabled":true,"description":"updated","type":"prompt",
		"prompt":{"template":"hi"}}}`
	rec = do(t, engine, http.MethodPut, "/v1/agent/customcommands/hello", update)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	var updated v1.UpdateCustomCommandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.GetCustomCommand().GetDescription() != "updated" {
		t.Errorf("description = %q", updated.GetCustomCommand().GetDescription())
	}

	rec = do(t, engine, http.MethodGet, "/v1/agent/customcommands", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var listed v1.ListCustomCommandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.GetCount() != 1 || len(listed.GetCustomCommands()) != 1 {
		t.Errorf("list = %d commands, count %d", len(listed.GetCustomCommands()), listed.GetCount())
	}

	rec = do(t, engine, http.MethodDelete, "/v1/agent/customcommands/hello", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, engine, http.MethodGet, "/v1/agent/customcommands/hello", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rec.Code)
	}
}

func TestCustomCommandContextRoute(t *testing.T) {
	engine := newCustomCommandTestEngine(t)
	rec := do(t, engine, http.MethodGet, "/v1/agent/agents/helper/context-commands", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("context-commands = %d: %s", rec.Code, rec.Body.String())
	}
	var listed v1.ListContextCustomCommandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.GetCustomCommands()) != 1 || listed.GetCustomCommands()[0].GetName() != "hello" {
		t.Errorf("context commands = %v", listed.GetCustomCommands())
	}
}

func TestCustomCommandInvokeRoute(t *testing.T) {
	engine := newCustomCommandTestEngine(t)
	rec := do(t, engine, http.MethodPost, "/v1/agent/customcommands/hello/invoke",
		`{"agent":"helper","params":{"name":"world"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("invoke = %d: %s", rec.Code, rec.Body.String())
	}
	var resp v1.InvokeCustomCommandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.GetStatus().GetPhase() != "Succeeded" {
		t.Errorf("phase = %q", resp.GetStatus().GetPhase())
	}
	if resp.GetStatus().GetPrompt().GetPrompt() != "rendered" {
		t.Errorf("prompt = %q", resp.GetStatus().GetPrompt().GetPrompt())
	}
}

func TestCustomCommandAuditRoute(t *testing.T) {
	engine := newCustomCommandTestEngine(t)
	rec := do(t, engine, http.MethodGet, "/v1/agent/customcommands/audits?command=hello&limit=10", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audits = %d: %s", rec.Code, rec.Body.String())
	}
	var listed v1.ListCustomCommandAuditResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listed.GetCount() != 1 || len(listed.GetAudits()) != 1 {
		t.Errorf("audits = %d, count %d", len(listed.GetAudits()), listed.GetCount())
	}
	if listed.GetAudits()[0].GetCommand() != "hello" {
		t.Errorf("command = %q", listed.GetAudits()[0].GetCommand())
	}
}

func TestCustomCommandApproveRoute(t *testing.T) {
	engine := newCustomCommandTestEngine(t)
	rec := do(t, engine, http.MethodPost, "/v1/agent/customcommands/hello/approve",
		`{"approvalId":"a1","decision":"allow","agent":"helper"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", rec.Code, rec.Body.String())
	}
	var resp v1.ApproveCustomCommandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.GetApprovalId() != "a1" || resp.GetStatus().GetPhase() != "Succeeded" {
		t.Errorf("approve = %+v", &resp)
	}
}

func TestCustomCommandRoutesAreNotOnTheAdminFace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(Deps{})
	engine := gin.New()
	h.ApplyAdminTo(engine.Group("/v1/admin"))
	rec := do(t, engine, http.MethodPost, "/v1/admin/custom-commands/hello/invoke", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("invoke on the admin face = %d, want 404", rec.Code)
	}
}
