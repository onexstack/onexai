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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

// guardedStore is the smallest AIStore that can answer an ownership question:
// every accessor is the disabled store's, except agents, whose Get returns the
// row under test.
type guardedStore struct {
	store.AIStore
	agent *model.AgentM
}

func (s guardedStore) Agents() store.AgentStore {
	return guardedAgentStore{AgentStore: s.AIStore.Agents(), row: s.agent}
}

type guardedAgentStore struct {
	store.AgentStore
	row *model.AgentM
}

func (s guardedAgentStore) Get(context.Context, *where.Options) (*model.AgentM, error) {
	if s.row == nil {
		return nil, store.ErrNotFound
	}
	return s.row, nil
}

// guardEngine wires the guard in front of a terminal handler that echoes the
// request method, path and body, so the tests can assert both the decision and
// what the guard rewrote.
func guardEngine(t *testing.T, h *Handler, method, path string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		// Stand in for UnifiedAuth: put the claims a signed bearer token would
		// have produced. The guard reads them from here.
		if raw := c.GetHeader("X-Test-Claims"); raw != "" {
			var claims bearer.Claims
			require.NoError(t, json.Unmarshal([]byte(raw), &claims))
			c.Set("jwt_claims", &claims)
		}
		c.Next()
	})
	engine.Use(h.UserGuard())
	engine.Handle(method, path, func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		c.JSON(http.StatusOK, gin.H{"body": string(body)})
	})
	return engine
}

func performGuarded(engine *gin.Engine, method, path string, claims *bearer.Claims) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(`{"agent":{"name":"probe","owner":"someone-else"}}`))
	if claims != nil {
		raw, _ := json.Marshal(claims)
		req.Header.Set("X-Test-Claims", string(raw))
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestUserGuardGovernanceStaysAdministratorOnly(t *testing.T) {
	h := &Handler{resources: store.NewEmptyStore()}

	t.Run("member is refused", func(t *testing.T) {
		engine := guardEngine(t, h, http.MethodGet, "/v1/core/secrets")
		rec := performGuarded(engine, http.MethodGet, "/v1/core/secrets",
			&bearer.Claims{UserID: "user-1", Provider: "local"})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), "administrator role required")
	})

	t.Run("administrator passes", func(t *testing.T) {
		engine := guardEngine(t, h, http.MethodGet, "/v1/core/secrets")
		rec := performGuarded(engine, http.MethodGet, "/v1/core/secrets",
			&bearer.Claims{UserID: "admin-1", Provider: "local", IsAdmin: true})
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("authorization rules are governance too", func(t *testing.T) {
		engine := guardEngine(t, h, http.MethodPost, "/v1/agent/authorizationrules")
		rec := performGuarded(engine, http.MethodPost, "/v1/agent/authorizationrules",
			&bearer.Claims{UserID: "user-1", Provider: "local"})
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
}

func TestUserGuardReadsAreOpenToMembers(t *testing.T) {
	h := &Handler{resources: store.NewEmptyStore()}
	engine := guardEngine(t, h, http.MethodGet, "/v1/agent/agents")

	rec := performGuarded(engine, http.MethodGet, "/v1/agent/agents",
		&bearer.Claims{UserID: "user-1", Provider: "local"})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUserGuardStampsTheCreatorOnCreate(t *testing.T) {
	h := &Handler{resources: store.NewEmptyStore()}
	engine := guardEngine(t, h, http.MethodPost, "/v1/agent/agents")

	rec := performGuarded(engine, http.MethodPost, "/v1/agent/agents",
		&bearer.Claims{UserID: "user-1", Name: "lkong", Provider: "local"})
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Body string `json:"body"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body.Body, `"owner":"user-1"`,
		"the caller must not be able to create a resource owned by somebody else")
}

func TestUserGuardWriteOwnership(t *testing.T) {
	writes := []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/v1/agent/agents/probe"},
		{http.MethodDelete, "/v1/agent/agents/probe"},
		{http.MethodPost, "/v1/agent/agents/probe/regenerate-prompt"},
	}

	for _, tc := range writes {
		t.Run(tc.method, func(t *testing.T) {
			owned := &Handler{resources: guardedStore{
				AIStore: store.NewEmptyStore(),
				agent:   &model.AgentM{Name: "probe", Owner: "user-1"},
			}}

			t.Run("owner passes", func(t *testing.T) {
				engine := guardEngine(t, owned, tc.method, tc.path)
				rec := performGuarded(engine, tc.method, tc.path,
					&bearer.Claims{UserID: "user-1", Provider: "local"})
				assert.Equal(t, http.StatusOK, rec.Code)
			})

			t.Run("somebody else is refused", func(t *testing.T) {
				engine := guardEngine(t, owned, tc.method, tc.path)
				rec := performGuarded(engine, tc.method, tc.path,
					&bearer.Claims{UserID: "user-2", Provider: "local"})
				assert.Equal(t, http.StatusForbidden, rec.Code)
			})

			t.Run("administrator passes", func(t *testing.T) {
				engine := guardEngine(t, owned, tc.method, tc.path)
				rec := performGuarded(engine, tc.method, tc.path,
					&bearer.Claims{UserID: "admin-1", Provider: "local", IsAdmin: true})
				assert.Equal(t, http.StatusOK, rec.Code)
			})
		})
	}
}

func TestUserGuardRecognizesTheDisplayNameAsOwner(t *testing.T) {
	// Rows created before account IDs were stamped carry the display name.
	h := &Handler{resources: guardedStore{
		AIStore: store.NewEmptyStore(),
		agent:   &model.AgentM{Name: "probe", Owner: "lkong"},
	}}
	engine := guardEngine(t, h, http.MethodPut, "/v1/agent/agents/probe")

	rec := performGuarded(engine, http.MethodPut, "/v1/agent/agents/probe",
		&bearer.Claims{UserID: "user-1", Name: "lkong", Provider: "local"})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUserGuardRefusesWritesToOwnerlessRows(t *testing.T) {
	h := &Handler{resources: guardedStore{
		AIStore: store.NewEmptyStore(),
		agent:   &model.AgentM{Name: "probe"},
	}}
	engine := guardEngine(t, h, http.MethodPut, "/v1/agent/agents/probe")

	rec := performGuarded(engine, http.MethodPut, "/v1/agent/agents/probe",
		&bearer.Claims{UserID: "user-1", Provider: "local"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestUserGuardLetsAnActionPathThroughWhenNoObjectExists(t *testing.T) {
	// POST /v1/agent/models/test is an action, not a model name. Treating a
	// failed lookup as "forbidden" would break it; the handler answers.
	h := &Handler{resources: store.NewEmptyStore()}
	engine := guardEngine(t, h, http.MethodPost, "/v1/agent/models/test")

	rec := performGuarded(engine, http.MethodPost, "/v1/agent/models/test",
		&bearer.Claims{UserID: "user-1", Provider: "local"})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUserGuardLeavesServiceCallersAlone(t *testing.T) {
	// No claims means an AK/SK caller: the guard must not invent an identity
	// or a role decision for it.
	h := &Handler{resources: store.NewEmptyStore()}
	engine := guardEngine(t, h, http.MethodPut, "/v1/agent/agents/probe")

	rec := performGuarded(engine, http.MethodPut, "/v1/agent/agents/probe", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}
