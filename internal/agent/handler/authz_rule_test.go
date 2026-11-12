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
	"bytes"
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
	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/authzrule"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// memRuleStore is an in-memory authzrule.Store for the HTTP-level test.
type memRuleStore struct {
	mu    sync.Mutex
	rules map[string]*model.AuthzRuleM
}

func newMemRuleStore() *memRuleStore { return &memRuleStore{rules: map[string]*model.AuthzRuleM{}} }

func (m *memRuleStore) List(context.Context, *where.Options) (int64, []*model.AuthzRuleM, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.AuthzRuleM, 0, len(m.rules))
	for _, r := range m.rules {
		out = append(out, ptr.Clone(r))
	}
	return int64(len(out)), out, nil
}

func (m *memRuleStore) Get(_ context.Context, opts *where.Options) (*model.AuthzRuleM, error) {
	name, _ := opts.Filters["name"].(string)
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rules[name]
	if !ok {
		return nil, agentstore.ErrNotFound
	}
	return ptr.Clone(r), nil
}

func (m *memRuleStore) Create(_ context.Context, r *model.AuthzRuleM) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules[r.Name] = ptr.Clone(r)
	return nil
}

func (m *memRuleStore) Update(_ context.Context, r *model.AuthzRuleM) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules[r.Name] = ptr.Clone(r)
	return nil
}

func (m *memRuleStore) Delete(_ context.Context, opts *where.Options) error {
	name, _ := opts.Filters["name"].(string)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rules, name)
	return nil
}

// newAuthzRuleTestEngine mounts only the authz-rule surface over a real Service
// backed by an in-memory store, so the test exercises routing, the 400/404
// mapping, and validation together.
func newAuthzRuleTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := authzrule.New(newMemRuleStore(), nil)
	h := NewHandler(Deps{
		Biz: biz.NewBiz(nil, nil, nil, nil, nil, nil, nil, nil, nil, svc, nil, nil, nil, nil, nil, nil, nil, nil),
	})
	engine := gin.New()

	// Both registries install every route; only the authz-rule routes are
	// touched here, and the others' dependencies are read only when served.
	v1 := engine.Group("/v1")
	h.ApplyTo(v1)
	h.ApplyUnifiedTo(v1)
	return engine
}

func doJSON(t *testing.T, engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	return w
}

func TestAuthzRuleEndpointsRejectInvalidEnumWith400(t *testing.T) {
	engine := newAuthzRuleTestEngine(t)

	w := doJSON(t, engine, http.MethodPost, "/v1/agent/authorizationrules", `{
		"authzRule": {
			"name": "bad",
			"subjectType": "person",
			"subjectIds": ["u"],
			"resourceKind": "agent",
			"resourceNames": ["*"],
			"verbs": ["read"],
			"effect": "allow",
			"status": "enabled"
		}
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid subjectType: got status %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	// The rule is validated in its canonical plural shape, so the blame names
	// subjects[0].kind. Accept either spelling: the point is that the response
	// names the field rather than saying "invalid request".
	body := w.Body.String()
	if !strings.Contains(body, "subjectType") && !strings.Contains(body, "subjects[0].kind") {
		t.Fatalf("400 must name the offending field, got %s", body)
	}
}

func TestAuthzRuleEndpointsCRUDRoundTrip(t *testing.T) {
	engine := newAuthzRuleTestEngine(t)
	body := `{
		"authzRule": {
			"name": "rule-1",
			"subjectType": "user",
			"subjectIds": ["u_alice"],
			"resourceKind": "agent",
			"resourceNames": ["default"],
			"agentNames": ["fz-public"],
			"verbs": ["read"],
			"effect": "allow",
			"status": "enabled"
		}
	}`

	if w := doJSON(t, engine, http.MethodPost, "/v1/agent/authorizationrules", body); w.Code != http.StatusOK {
		t.Fatalf("create: got %d, body %s", w.Code, w.Body.String())
	}
	// The Agent scope has to survive the write, not just the bind: a rule
	// created through this API and read back as unscoped would answer for every
	// Agent, which is the opposite of what the caller asked for.
	if w := doJSON(t, engine, http.MethodGet, "/v1/agent/authorizationrules/rule-1", ""); w.Code != http.StatusOK {
		t.Fatalf("get after create: got %d, body %s", w.Code, w.Body.String())
	} else if got := w.Body.String(); !strings.Contains(got, `"agentNames":["fz-public"]`) {
		t.Fatalf("the Agent scope did not survive the round trip: %s", got)
	}
	if w := doJSON(t, engine, http.MethodGet, "/v1/agent/authorizationrules", ""); w.Code != http.StatusOK {
		t.Fatalf("list: got %d", w.Code)
	}
	if w := doJSON(t, engine, http.MethodDelete, "/v1/agent/authorizationrules/rule-1", ""); w.Code != http.StatusOK {
		t.Fatalf("delete: got %d, body %s", w.Code, w.Body.String())
	}
	if w := doJSON(t, engine, http.MethodGet, "/v1/agent/authorizationrules/rule-1", ""); w.Code != http.StatusNotFound {
		t.Fatalf("get after delete: got %d, want 404", w.Code)
	}
}

func TestAuthzRuleEndpointsUpdateMissingIs404(t *testing.T) {
	engine := newAuthzRuleTestEngine(t)
	w := doJSON(t, engine, http.MethodPut, "/v1/agent/authorizationrules/ghost", `{
		"authzRule": {
			"subjectType": "user", "subjectIds": ["u"], "resourceKind": "agent",
			"resourceNames": ["*"], "verbs": ["read"], "effect": "allow", "status": "enabled"
		}
	}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("update of missing rule: got %d, want 404 (body %s)", w.Code, w.Body.String())
	}
}

func TestAuthzRuleEndpointsUnavailableWhenServiceNil(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(Deps{Biz: biz.NewBiz(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)})
	engine := gin.New()
	h.ApplyTo(engine.Group("/v1"))
	h.ApplyUnifiedTo(engine.Group("/v1"))

	w := doJSON(t, engine, http.MethodGet, "/v1/agent/authorizationrules", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("nil service: got %d, want 500 (body %s)", w.Code, w.Body.String())
	}
	// The response is still well-formed JSON, not a panic trace.
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
}
