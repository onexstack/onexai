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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/validation"
	"github.com/onexstack/onexai/internal/agent/store"
)

// recordingAgents is an AgentStore that records the names it was asked to
// create. Every other method comes from the embedded (nil) interface: this test
// only drives the create path.
type recordingAgents struct {
	store.AgentStore
	created     []string
	createdRows []model.AgentM
	updated     []string
}

func (a *recordingAgents) Create(_ context.Context, m *model.AgentM) error {
	a.created = append(a.created, m.Name)
	a.createdRows = append(a.createdRows, *m)
	return nil
}

func (a *recordingAgents) Update(_ context.Context, m *model.AgentM) error {
	a.updated = append(a.updated, m.Name)
	return nil
}

// recordingResources overrides only the agents accessor, so a test can hand the
// handler a store without implementing the whole resource surface.
type recordingResources struct {
	store.AIStore
	agents *recordingAgents
}

func (r *recordingResources) Agents() store.AgentStore { return r.agents }

// TestCreateAgentValidationAlwaysRuns pins the contract the alignment with
// miniblog establishes: validation is part of the endpoint, not a deployment
// switch, so a nameless agent is refused and never reaches the store.
func TestCreateAgentValidationAlwaysRuns(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		body        string
		wantStatus  int
		wantCreated int
	}{
		{
			name:        "nameless agent is refused",
			body:        `{"agent":{}}`,
			wantStatus:  http.StatusBadRequest,
			wantCreated: 0,
		},
		{
			name:        "missing payload is refused",
			body:        `{}`,
			wantStatus:  http.StatusBadRequest,
			wantCreated: 0,
		},
		{
			name:        "named agent is accepted",
			body:        `{"agent":{"name":"helper"}}`,
			wantStatus:  http.StatusOK,
			wantCreated: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agents := &recordingAgents{}
			resources := &recordingResources{agents: agents}
			h := NewHandler(Deps{
				Biz:       newTestBiz(resources),
				Resources: resources,
				Validator: validation.New(nil),
			})

			// Only the create route is mounted: the package-level registrar
			// table would also install surfaces this focused handler has no
			// dependencies for.
			router := gin.New()
			router.Group("/v1").Group("/agent/agents").POST("", h.CreateAgent)

			req := httptest.NewRequest(http.MethodPost, "/v1/agent/agents", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", w.Code, tt.wantStatus, w.Body.String())
			}
			if len(agents.created) != tt.wantCreated {
				t.Errorf("created %d agents, want %d", len(agents.created), tt.wantCreated)
			}
		})
	}
}

// TestAgentDelegatesPresenceSurvivesJSON pins the clear semantics the console
// depends on: an explicit "delegates": [] must reach the store as a non-nil
// empty list, while an omitted key must stay nil ("no opinion").
func TestAgentDelegatesPresenceSurvivesJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		body string
		want []string
	}{
		{name: "explicit empty clears", body: `{"agent":{"name":"helper","delegates":[]}}`, want: []string{}},
		{name: "absent is no opinion", body: `{"agent":{"name":"helper"}}`, want: nil},
		{name: "ordered list", body: `{"agent":{"name":"helper","delegates":[{"name":"researcher"},{"name":"reviewer"}]}}`,
			want: []string{"helper-public/researcher", "helper-public/reviewer"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agents := &recordingAgents{}
			resources := &recordingResources{agents: agents}
			h := NewHandler(Deps{
				Biz:       newTestBiz(resources),
				Resources: resources,
				Validator: validation.New(nil),
			})

			router := gin.New()
			router.Group("/v1").Group("/agent/agents").POST("", h.CreateAgent)

			req := httptest.NewRequest(http.MethodPost, "/v1/agent/agents", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
			}
			if len(agents.createdRows) != 1 {
				t.Fatalf("created %d rows, want 1", len(agents.createdRows))
			}
			got := agents.createdRows[0].Delegates
			if tt.want == nil {
				if got != nil {
					t.Fatalf("delegates = %#v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("delegates = nil, want non-nil %#v", tt.want)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("delegates = %#v, want %#v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("delegates = %#v, want %#v", got, tt.want)
				}
			}
		})
	}
}

// TestUpdateAgentUsesThePathName pins the binder contract: the Update message
// has no identity field, so the path is the only source of the name. Validation
// must see the path value, not the empty body.
func TestUpdateAgentUsesThePathName(t *testing.T) {
	gin.SetMode(gin.TestMode)

	agents := &recordingAgents{}
	resources := &recordingResources{agents: agents}
	h := NewHandler(Deps{
		Biz:       newTestBiz(resources),
		Resources: resources,
		Validator: validation.New(nil),
	})

	router := gin.New()
	router.Group("/v1").Group("/agent/agents").PUT("/:name", h.UpdateAgent)

	req := httptest.NewRequest(http.MethodPut, "/v1/agent/agents/demo",
		strings.NewReader(`{"agent":{"description":"updated"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, http.StatusOK, w.Body.String())
	}
	if len(agents.updated) != 1 || agents.updated[0] != "demo" {
		t.Errorf("updated names = %v, want [demo]", agents.updated)
	}
}
