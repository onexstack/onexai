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
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
)

// pagedAgents serves a fixed fixture through the paged read.
type pagedAgents struct {
	store.AgentStore
	all []*model.AgentM
}

func (a *pagedAgents) List(_ context.Context, opts *where.Options) (int64, []*model.AgentM, error) {
	offset, limit := 0, -1
	if opts != nil {
		offset, limit = opts.Offset, opts.Limit
	}
	total, page := store.Page(a.all, offset, limit)
	return total, page, nil
}

type pagedResources struct {
	store.AIStore
	agents *pagedAgents
}

func (r *pagedResources) Agents() store.AgentStore { return r.agents }

// TestListAgentPagination pins both halves of the list contract: a request
// without paging parameters still returns every record, and one that asks for a
// page gets that page plus the total. The count has to be the *total*, not the
// page length — the page size is what the caller already knows.
func TestListAgentPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)

	agents := &pagedAgents{all: []*model.AgentM{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}}
	resources := &pagedResources{agents: agents}
	h := NewHandler(Deps{Biz: newTestBiz(resources), Resources: resources})

	router := gin.New()
	router.Group("/v1").Group("/agent/agents").GET("", h.ListAgent)

	tests := []struct {
		name      string
		query     string
		wantNames []string
		wantCount float64
	}{
		{name: "no parameters returns everything", query: "", wantNames: []string{"a", "b", "c"}, wantCount: 3},
		{name: "first page", query: "?limit=2", wantNames: []string{"a", "b"}, wantCount: 3},
		{name: "second page", query: "?offset=2&limit=2", wantNames: []string{"c"}, wantCount: 3},
		{name: "offset past the end", query: "?offset=9&limit=2", wantNames: []string{}, wantCount: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/agent/agents"+tt.query, nil))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
			}

			var got struct {
				Agents []struct {
					Name string `json:"name"`
				} `json:"agents"`
				Count float64 `json:"count"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode response: %v (body %s)", err, w.Body.String())
			}

			if len(got.Agents) != len(tt.wantNames) {
				t.Fatalf("got %d agents, want %d (body %s)", len(got.Agents), len(tt.wantNames), w.Body.String())
			}
			for i, want := range tt.wantNames {
				if got.Agents[i].Name != want {
					t.Errorf("agent %d = %q, want %q", i, got.Agents[i].Name, want)
				}
			}
			if got.Count != tt.wantCount {
				t.Errorf("count = %v, want %v", got.Count, tt.wantCount)
			}
		})
	}
}

// TestListAgentRejectsBadPaging protects the bind step: a limit that is not a
// number is a malformed request, not something to silently ignore.
func TestListAgentRejectsBadPaging(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resources := &pagedResources{agents: &pagedAgents{}}
	h := NewHandler(Deps{Biz: newTestBiz(resources), Resources: resources})
	router := gin.New()
	router.Group("/v1").Group("/agent/agents").GET("", h.ListAgent)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/agent/agents?limit=abc", nil))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
}
