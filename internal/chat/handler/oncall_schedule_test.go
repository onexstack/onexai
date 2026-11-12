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
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

// TestOncallRoutesAuthenticateAndGateWrites pins the split the new routes
// introduced: the rota is readable by any signed-in operator and writable only
// by an administrator. The handlers run with no dependencies here, so an
// authorized request recovers into a 500; the assertion is deliberately "the
// gate did not refuse it", not a status the biz layer would choose.
func TestOncallRoutesAuthenticateAndGateWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(gin.Recovery())
	h := &Handler{JWTSecret: ticketTestSecret}
	h.ApplyTo(engine.Group("/v1"))

	member := ticketToken(t, bearer.Claims{UserID: "u1", Provider: "local"})
	admin := ticketToken(t, bearer.Claims{UserID: "a1", Provider: "local", IsAdmin: true})

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/oncall-groups"},
		{http.MethodGet, "/v1/oncall-groups/:name"},
		{http.MethodGet, "/v1/oncall-groups/:name/schedule"},
		{http.MethodGet, "/v1/oncall-groups/:name/schedule/oncall"},
		{http.MethodGet, "/v1/oncall-groups/:name/schedule/timeline"},
		{http.MethodGet, "/v1/oncall-groups/:name/schedule/layer-timelines"},
		{http.MethodGet, "/v1/oncall-groups/:name/schedule/overrides"},
		{http.MethodGet, "/v1/oncall-groups/:name/schedule/unavailabilities"},
		{http.MethodGet, "/v1/oncall-groups/:name/schedule/holidays"},
		{http.MethodGet, "/v1/oncall-groups/:name/escalation"},
		{http.MethodGet, "/v1/oncall-current"},
		{http.MethodPut, "/v1/oncall-groups/:name/schedule"},
		{http.MethodPost, "/v1/oncall-groups/:name/schedule/preview"},
		{http.MethodPost, "/v1/oncall-groups/:name/schedule/overrides"},
		{http.MethodDelete, "/v1/oncall-groups/:name/schedule/overrides/:overrideID"},
		{http.MethodPost, "/v1/oncall-groups/:name/schedule/unavailabilities"},
		{http.MethodDelete, "/v1/oncall-groups/:name/schedule/unavailabilities/:unavailabilityID"},
		{http.MethodPost, "/v1/oncall-groups/:name/schedule/holidays"},
		{http.MethodDelete, "/v1/oncall-groups/:name/schedule/holidays/:holidayID"},
		{http.MethodPut, "/v1/oncall-groups/:name/escalation"},
		{http.MethodPost, "/v1/oncall-groups/:name/escalate"},
	}
	for _, route := range routes {
		assert.True(t, routeExists(engine, route.method, route.path),
			"%s %s is not registered", route.method, route.path)
	}

	// Every route rejects an unauthenticated caller before the handler runs.
	for _, route := range routes {
		assert.Equal(t, http.StatusUnauthorized, oncallRequest(engine, route.method, concrete(route.path), "").Code,
			"%s %s without a token", route.method, route.path)
	}

	// Reads are open to a signed-in member; writes are not.
	reads := []string{
		"/v1/oncall-groups",
		"/v1/oncall-groups/go",
		"/v1/oncall-groups/go/schedule",
		"/v1/oncall-groups/go/schedule/oncall",
		"/v1/oncall-groups/go/schedule/overrides",
		"/v1/oncall-groups/go/escalation",
		"/v1/oncall-current",
	}
	for _, path := range reads {
		rec := oncallRequest(engine, http.MethodGet, path, member)
		assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "GET %s", path)
		assert.NotEqual(t, http.StatusForbidden, rec.Code, "GET %s refused a member", path)
	}

	writes := []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/v1/oncall-groups/go/schedule"},
		{http.MethodPost, "/v1/oncall-groups/go/schedule/preview"},
		{http.MethodPost, "/v1/oncall-groups/go/schedule/overrides"},
		{http.MethodDelete, "/v1/oncall-groups/go/schedule/overrides/o1"},
		{http.MethodPost, "/v1/oncall-groups/go/schedule/unavailabilities"},
		{http.MethodDelete, "/v1/oncall-groups/go/schedule/unavailabilities/u1"},
		{http.MethodPost, "/v1/oncall-groups/go/schedule/holidays"},
		{http.MethodDelete, "/v1/oncall-groups/go/schedule/holidays/h1"},
		{http.MethodPut, "/v1/oncall-groups/go/escalation"},
		{http.MethodPost, "/v1/oncall-groups/go/escalate"},
	}
	for _, route := range writes {
		assert.Equal(t, http.StatusForbidden, oncallRequest(engine, route.method, route.path, member).Code,
			"%s %s let a member write", route.method, route.path)
	}

	// An administrator passes the gate (the empty handler then recovers).
	for _, route := range writes {
		rec := oncallRequest(engine, route.method, route.path, admin)
		assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "%s %s", route.method, route.path)
		assert.NotEqual(t, http.StatusForbidden, rec.Code, "%s %s refused an admin", route.method, route.path)
	}
}

func oncallRequest(
	engine *gin.Engine, method, path, token string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func concrete(path string) string {
	replacer := strings.NewReplacer(
		":name", "go",
		":overrideID", "o1",
		":unavailabilityID", "u1",
		":holidayID", "h1",
	)
	return replacer.Replace(path)
}
