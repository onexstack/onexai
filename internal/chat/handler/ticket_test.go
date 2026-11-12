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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

// ticketTestSecret signs the tokens below. Not a credential: it lives in this
// file and signs nothing outside the test.
const ticketTestSecret = "ticket-test-secret"

// TestTicketRoutesRequireAnAdministrator pins the gate on the read surface.
//
// # Why this is a test and not just a live check
//
// The gate is applied by whoever builds the route group, so a route mounted
// without it is refused by nothing — it answers, returns real ticket data, and
// nothing fails. That is the failure mode this whole repository keeps writing
// comments about, and a live curl verified it exactly once, on one afternoon.
//
// The neighbouring /v1/oncall-groups routes used to carry no role check at all.
// That gap is now closed (reads require a session, writes require an
// administrator) and pinned in oncall_schedule_test.go.
func TestTicketRoutesRequireAnAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	// Recovery, so the handler running with no dependencies panics into a 500
	// instead of taking the test binary down.
	engine.Use(gin.Recovery())
	h := &Handler{JWTSecret: ticketTestSecret}
	h.ApplyTo(engine.Group("/v1"))

	// The route must exist: a gate test on a route nobody mounted would pass
	// while asserting nothing.
	require.True(t, routeExists(engine, http.MethodGet, "/v1/tickets"),
		"GET /v1/tickets is not registered")

	member := ticketToken(t, bearer.Claims{UserID: "u1", Provider: "local"})
	admin := ticketToken(t, bearer.Claims{UserID: "a1", Provider: "local", IsAdmin: true})

	t.Run("no token", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, ticketGet(engine, "").Code)
	})

	t.Run("not an administrator", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden, ticketGet(engine, member).Code,
			"a caller without the admin claim must not read tickets")
	})

	t.Run("administrator", func(t *testing.T) {
		rec := ticketGet(engine, admin)
		assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
		assert.NotEqual(t, http.StatusForbidden, rec.Code,
			"the gate refused an administrator")
	})

	t.Run("detail route", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden, ticketGet(engine, member, "/v1/tickets/tkt-1").Code)
		assert.Equal(t, http.StatusUnauthorized, ticketGet(engine, "", "/v1/tickets/tkt-1").Code)
	})

	t.Run("handling log route", func(t *testing.T) {
		require.True(t, routeExists(engine, http.MethodGet, "/v1/tickets/:ticketID/notes"),
			"GET /v1/tickets/:ticketID/notes is not registered")

		assert.Equal(t, http.StatusForbidden, ticketGet(engine, member, "/v1/tickets/tkt-1/notes").Code)
		assert.Equal(t, http.StatusUnauthorized, ticketGet(engine, "", "/v1/tickets/tkt-1/notes").Code)
	})
}

func routeExists(engine *gin.Engine, method, path string) bool {
	for _, route := range engine.Routes() {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}

func ticketGet(engine *gin.Engine, token string, paths ...string) *httptest.ResponseRecorder {
	path := "/v1/tickets"
	if len(paths) > 0 {
		path = paths[0]
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func ticketToken(t *testing.T, claims bearer.Claims) string {
	t.Helper()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(ticketTestSecret))
	require.NoError(t, err)
	return token
}
