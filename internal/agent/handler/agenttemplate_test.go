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
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// memAgentTemplateStore is an in-memory AgentTemplateStore for the HTTP test.
type memAgentTemplateStore struct {
	mu   sync.Mutex
	rows map[string]*model.AgentTemplateM
}

func newMemAgentTemplateStore() *memAgentTemplateStore {
	return &memAgentTemplateStore{rows: map[string]*model.AgentTemplateM{}}
}

func (m *memAgentTemplateStore) List(context.Context, *where.Options) (int64, []*model.AgentTemplateM, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.AgentTemplateM, 0, len(m.rows))
	for _, row := range m.rows {
		out = append(out, ptr.Clone(row))
	}
	return int64(len(out)), out, nil
}

func (m *memAgentTemplateStore) Get(_ context.Context, opts *where.Options) (*model.AgentTemplateM, error) {
	name, _ := opts.Filters["name"].(string)
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[name]
	if !ok {
		return nil, agentstore.ErrNotFound
	}
	return ptr.Clone(row), nil
}

func (m *memAgentTemplateStore) Create(_ context.Context, row *model.AgentTemplateM) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[row.Name] = ptr.Clone(row)
	return nil
}

func (m *memAgentTemplateStore) Update(_ context.Context, row *model.AgentTemplateM) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[row.Name] = ptr.Clone(row)
	return nil
}

func (m *memAgentTemplateStore) Delete(_ context.Context, opts *where.Options) error {
	name, _ := opts.Filters["name"].(string)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, name)
	return nil
}

type agentTemplateResources struct {
	agentstore.AIStore
	templates *memAgentTemplateStore
}

func (r agentTemplateResources) AgentTemplates() agentstore.AgentTemplateStore { return r.templates }

func newAgentTemplateTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	resources := agentTemplateResources{templates: newMemAgentTemplateStore()}
	h := NewHandler(Deps{Biz: newTestBiz(resources), Resources: resources})
	engine := gin.New()
	h.ApplyTo(engine.Group("/v1"))
	h.ApplyUnifiedTo(engine.Group("/v1"))
	return engine
}

func TestAgentTemplateCRUDRoundTrip(t *testing.T) {
	engine := newAgentTemplateTestEngine(t)

	create := `{"agentTemplate":{"name":"base","displayName":"Base","spec":"{\"enabled\":true,\"skills\":[{\"name\":\"triage\"}]}"}}`
	rec := do(t, engine, http.MethodPost, "/v1/agent/templates", create)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created v1.CreateAgentTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.GetAgentTemplate().GetName() != "base" {
		t.Errorf("name = %q", created.GetAgentTemplate().GetName())
	}
	if created.GetAgentTemplate().GetStatus() != model.StatusEnabled {
		t.Errorf("status = %q, want %s", created.GetAgentTemplate().GetStatus(), model.StatusEnabled)
	}

	rec = do(t, engine, http.MethodGet, "/v1/agent/templates/base", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rec.Code, rec.Body.String())
	}
	var got v1.GetAgentTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.GetAgentTemplate().GetSpec() != `{"enabled":true,"skills":[{"name":"triage"}]}` {
		t.Errorf("spec = %q", got.GetAgentTemplate().GetSpec())
	}

	update := `{"agentTemplate":{"name":"base","spec":"{\"enabled\":false,\"skills\":[{\"name\":\"triage\"},{\"name\":\"deploy\"}]}"}}`
	rec = do(t, engine, http.MethodPut, "/v1/agent/templates/base", update)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	var updated v1.UpdateAgentTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.GetAgentTemplate().GetStatus() != model.StatusDisabled {
		t.Errorf("status = %q, want the spec's disabled state", updated.GetAgentTemplate().GetStatus())
	}

	rec = do(t, engine, http.MethodGet, "/v1/agent/templates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var listed v1.ListAgentTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.GetCount() != 1 || len(listed.GetAgentTemplates()) != 1 {
		t.Errorf("list = %d/%d, want 1", listed.GetCount(), len(listed.GetAgentTemplates()))
	}

	rec = do(t, engine, http.MethodDelete, "/v1/agent/templates/base", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, engine, http.MethodGet, "/v1/agent/templates/base", "")
	if rec.Code == http.StatusOK {
		t.Fatal("get after delete returned 200")
	}
}

func TestAgentTemplateRejectsMalformedSpec(t *testing.T) {
	engine := newAgentTemplateTestEngine(t)

	rec := do(t, engine, http.MethodPost, "/v1/agent/templates",
		`{"agentTemplate":{"name":"bad","spec":"not json"}}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("create with a malformed spec returned 200: %s", rec.Body.String())
	}
}
