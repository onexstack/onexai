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
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentstore "github.com/onexstack/onexai/internal/agent/store"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// memAgentBindings is the in-memory AgentBindingStore the CRUD test drives.
type memAgentBindings struct {
	agentstore.AgentBindingStore
	mu   sync.Mutex
	rows map[string]*agentv1alpha1.AgentBinding
}

func newMemAgentBindings() *memAgentBindings {
	return &memAgentBindings{rows: map[string]*agentv1alpha1.AgentBinding{}}
}

func (m *memAgentBindings) Create(
	_ context.Context, obj *agentv1alpha1.AgentBinding,
) (*agentv1alpha1.AgentBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := obj.DeepCopy()
	m.rows[stored.Name] = stored
	return stored.DeepCopy(), nil
}

func (m *memAgentBindings) Update(
	_ context.Context, obj *agentv1alpha1.AgentBinding,
) (*agentv1alpha1.AgentBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.rows[obj.Name]
	if !ok {
		return nil, agentstore.ErrNotFound
	}
	stored.Spec = obj.Spec
	stored.SetLabels(obj.Labels)
	return stored.DeepCopy(), nil
}

func (m *memAgentBindings) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, name)
	return nil
}

func (m *memAgentBindings) Get(_ context.Context, name string) (*agentv1alpha1.AgentBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.rows[name]
	if !ok {
		return nil, agentstore.ErrNotFound
	}
	return stored.DeepCopy(), nil
}

func (m *memAgentBindings) List(
	_ context.Context, offset, limit int,
) (int64, []*agentv1alpha1.AgentBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*agentv1alpha1.AgentBinding, 0, len(m.rows))
	for _, row := range m.rows {
		out = append(out, row.DeepCopy())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	total := int64(len(out))
	if offset > 0 {
		if offset >= len(out) {
			out = nil
		} else {
			out = out[offset:]
		}
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return total, out, nil
}

// bindingResources overrides one accessor on the otherwise nil store.
type bindingResources struct {
	agentstore.AIStore
	bindings *memAgentBindings
}

func (r bindingResources) AgentBindings() agentstore.AgentBindingStore { return r.bindings }

func newCRDTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	resources := bindingResources{bindings: newMemAgentBindings()}
	h := NewHandler(Deps{
		Biz:       newTestBiz(resources),
		Resources: resources,
	})
	engine := gin.New()
	h.ApplyUnifiedTo(engine.Group("/v1"))
	return engine
}

// TestRouteFacesDoNotConflict mounts the three faces on the prefixes the real
// server uses. Gin panics on a duplicate method+path, so this is the test that
// catches a resource route accidentally registered on both the signed and the
// unified face.
func TestRouteFacesDoNotConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(Deps{})
	engine := gin.New()

	require.NotPanics(t, func() {
		h.ApplyTo(engine.Group("/v1"))
		h.ApplyUnifiedTo(engine.Group("/v1"))
		h.ApplyAdminTo(engine.Group("/v1/admin"))
	})
}

func TestDeclarativeResourceRoutesAreOnTheUnifiedFace(t *testing.T) {
	unified := routeKeys(t, func(g *gin.RouterGroup, h *Handler) { h.ApplyUnifiedTo(g) })
	signed := routeKeys(t, func(g *gin.RouterGroup, h *Handler) { h.ApplyTo(g) })
	admin := routeKeys(t, func(g *gin.RouterGroup, h *Handler) { h.ApplyAdminTo(g) })

	for _, resource := range []string{
		"/agent/agentbindings",
		"/agent/referencegrants",
		"/agent/runtimeprofiles",
		"/agent/runtimehostings",
		"/agent/sandboxtemplates",
		"/agent/scheduledruns",
	} {
		for _, key := range []string{
			"POST " + resource,
			"GET " + resource,
			"GET " + resource + "/:name",
			"PUT " + resource + "/:name",
			"DELETE " + resource + "/:name",
		} {
			assert.Containsf(t, unified, key, "unified face is missing %s", key)
			assert.NotContainsf(t, signed, key, "resource route %s leaked onto the signed-only face", key)
			assert.NotContainsf(t, admin, key, "resource route %s leaked onto the admin-only face", key)
		}
	}
}

func TestAgentBindingCRUDRoundTrip(t *testing.T) {
	engine := newCRDTestEngine(t)

	create := `{"agentBinding":{"name":"base","namespace":"default","spec":{` +
		`"agents":["helper"],"commands":["bind"],"prompts":[{"name":"base-prompt"}]}}}`
	rec := do(t, engine, http.MethodPost, "/v1/agent/agentbindings", create)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var created v1.CreateAgentBindingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotNil(t, created.GetAgentBinding())
	assert.Equal(t, "base", created.GetAgentBinding().GetName())
	assert.Equal(t, []string{"helper"}, created.GetAgentBinding().GetSpec().GetAgents())
	assert.Equal(t, "base-prompt", created.GetAgentBinding().GetSpec().GetPrompts()[0].GetName())

	rec = do(t, engine, http.MethodGet, "/v1/agent/agentbindings/base", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	update := `{"agentBinding":{"name":"base","namespace":"default","spec":{` +
		`"description":"updated","agents":["helper"]}}}`
	rec = do(t, engine, http.MethodPut, "/v1/agent/agentbindings/base", update)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var updated v1.UpdateAgentBindingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
	assert.Equal(t, "updated", updated.GetAgentBinding().GetSpec().GetDescription())

	rec = do(t, engine, http.MethodGet, "/v1/agent/agentbindings?offset=0&limit=10", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var listed v1.ListAgentBindingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	assert.Equal(t, int64(1), listed.GetCount())
	assert.Len(t, listed.GetAgentBindings(), 1)

	rec = do(t, engine, http.MethodDelete, "/v1/agent/agentbindings/base", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(t, engine, http.MethodGet, "/v1/agent/agentbindings/base", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestDeclarativeResourceRejectsMalformedJSON(t *testing.T) {
	engine := newCRDTestEngine(t)
	rec := do(t, engine, http.MethodPost, "/v1/agent/runtimeprofiles", "{not-json")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestLegacyUngroupedRoutesAreGone(t *testing.T) {
	// Each face gets its own router: the signed and admin faces deliberately
	// share a relative path (/agent/ask), so registering both on one engine
	// would be a genuine conflict.
	signed := routeKeys(t, func(g *gin.RouterGroup, h *Handler) { h.ApplyTo(g) })
	unified := routeKeys(t, func(g *gin.RouterGroup, h *Handler) { h.ApplyUnifiedTo(g) })
	admin := routeKeys(t, func(g *gin.RouterGroup, h *Handler) { h.ApplyAdminTo(g) })
	all := append(append(append([]string{}, signed...), unified...), admin...)
	joined := strings.Join(all, "\n")
	for _, legacy := range []string{
		"GET /agents",
		"GET /skills",
		"GET /knowledge-bases",
		"GET /authz-rules",
		"GET /secrets",
		"GET /admin/skills",
	} {
		assert.NotContainsf(t, joined, legacy, "legacy route %q is still registered", legacy)
	}
}
