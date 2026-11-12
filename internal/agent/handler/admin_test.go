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
	"sort"
	"strings"
	"testing"
	"time"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/pkg/conversion"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

// adminTestSecret is the HMAC secret the test tokens are signed with. It is not
// a credential: it never leaves this file and signs nothing outside the test.
const adminTestSecret = "admin-test-secret"

// managementOnlyRoutes are routes the management surface owns outright.
//
// The signed face already has an asynchronous-run contract (/v1/agent/runs),
// but it is a dispatch API: there is no global list, and the console's
// list/detail/events/cancel/retry are management concerns with no
// service-to-service caller. They are listed here rather than contorted into a
// signed twin that would widen that face for nobody.
var managementOnlyRoutes = map[string]bool{
	"GET /agent/runs":             true,
	"GET /agent/runs/monitor":     true,
	"GET /agent/runs/stuck":       true,
	"GET /agent/runs/:id":         true,
	"GET /agent/runs/:id/events":  true,
	"POST /agent/runs/:id/cancel": true,
	"POST /agent/runs/:id/retry":  true,

	// The control plane's own extension objects (see extensions.go). They are
	// administrative configuration of the API server this platform runs on; a
	// service-to-service caller has no reason to rewrite an admission webhook,
	// and a signed twin would hand one the ability to change what every request
	// is subject to.
	"GET /extensions":                true,
	"GET /extensions/:kind":          true,
	"POST /extensions/:kind":         true,
	"GET /extensions/:kind/:name":    true,
	"PUT /extensions/:kind/:name":    true,
	"DELETE /extensions/:kind/:name": true,
}

// TestAdminRoutesAreASubsetOfTheSignedOnes pins the relationship between the two
// faces.
//
// # Why a subset and not equality
//
// The management surface re-mounts the resource routes and the debug run, and
// deliberately none of the rest: the OpenAI-compatible endpoint, the A2A
// transports and the observability proxies are not things a browser console
// has any business calling, and giving them an admin twin would widen the
// exposed surface for no caller. So the invariant is one-directional — every
// management route must exist on the signed face, at the same relative path —
// and it is stated as such rather than as an equality the code would have to be
// contorted to satisfy.
//
// A failure here means a route was added to one face and not the other, which is
// the drift that has no other guard: the two tables are written out separately
// on purpose (see admin.go), and nothing but this test makes them move together.
func TestAdminRoutesAreASubsetOfTheSignedOnes(t *testing.T) {
	signed := routeKeys(t, func(g *gin.RouterGroup, h *Handler) { h.ApplyTo(g) })
	admin := routeKeys(t, func(g *gin.RouterGroup, h *Handler) { h.ApplyAdminTo(g) })

	require.NotEmpty(t, signed, "the signed face registered no routes; this test would pass vacuously")
	require.NotEmpty(t, admin, "the management surface registered no routes")
	// Logged rather than asserted: the counts are a fact about two route tables
	// that a reader of a failure wants, and a hard-coded number here would break
	// on every addition while proving nothing the subset check does not.
	t.Logf("signed face: %d routes; management surface: %d", len(signed), len(admin))

	for _, key := range admin {
		if managementOnlyRoutes[key] {
			continue
		}
		assert.Containsf(t, signed, key,
			"the management surface serves %s but the signed face does not", key)
	}
}

// TestAdminRoutesRequireAnAdministrator is the guard the plan called for: it
// walks every registered management route and refuses it twice — once with no
// token, once with a valid token that is not an administrator.
//
// # What it is really testing
//
// The gate lives on the route group, which means it is applied by whoever
// created the group — and a route mounted on the wrong group, or a new group
// built without the middleware, is refused by nothing at all. That failure is
// silent in exactly the way this repository keeps writing comments about: the
// route answers, returns real data, and nobody notices it was public. Walking
// the registered table is what turns that into a red test.
//
// The administrator case is asserted only as "not refused": what happens after
// the gate is the handler's business, and this test's Handler has no dependencies
// to run with.
func TestAdminRoutesRequireAnAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Recovery, so a handler that runs with no dependencies panics into a 500
	// rather than taking the test binary down with it.
	engine := gin.New()
	engine.Use(gin.Recovery())
	h := &Handler{}
	admin := engine.Group("/v1/admin", bearer.AuthWithMode(adminTestSecret, false, ""), bearer.RequireAdmin())
	h.ApplyAdminTo(admin)

	routes := engine.Routes()
	require.NotEmpty(t, routes, "no management route was registered")

	member := signedToken(t, bearer.Claims{UserID: "user-1", Provider: "local"})
	administrator := signedToken(t, bearer.Claims{UserID: "admin-1", Provider: "local", IsAdmin: true})

	for _, route := range routes {
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			path := concretePath(route.Path)

			t.Run("no token", func(t *testing.T) {
				rec := perform(engine, route.Method, path, "")
				assert.Equal(t, http.StatusUnauthorized, rec.Code,
					"a request with no token must not reach %s", route.Path)
			})

			t.Run("not an administrator", func(t *testing.T) {
				rec := perform(engine, route.Method, path, member)
				assert.Equal(t, http.StatusForbidden, rec.Code,
					"a caller without the admin claim must not reach %s", route.Path)
			})

			t.Run("administrator", func(t *testing.T) {
				rec := perform(engine, route.Method, path, administrator)
				assert.NotEqual(t, http.StatusUnauthorized, rec.Code,
					"the token check refused an administrator at %s", route.Path)
				assert.NotEqual(t, http.StatusForbidden, rec.Code,
					"the role check refused an administrator at %s", route.Path)
			})
		})
	}
}

// routeKeys registers one face's routes and returns its method+relative-path set.
func routeKeys(t *testing.T, apply func(*gin.RouterGroup, *Handler)) []string {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	h := &Handler{}
	apply(engine.Group("", gin.HandlerFunc(func(c *gin.Context) { c.Next() })), h)

	keys := make([]string, 0, len(engine.Routes()))
	for _, route := range engine.Routes() {
		keys = append(keys, route.Method+" "+route.Path)
	}
	sort.Strings(keys)
	return keys
}

// concretePath replaces a router's parameter placeholders with a literal, so a
// route table can be requested without knowing any real identifier.
func concretePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*") {
			segments[i] = "x"
		}
	}
	return strings.Join(segments, "/")
}

func perform(engine *gin.Engine, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func signedToken(t *testing.T, claims bearer.Claims) string {
	t.Helper()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(adminTestSecret))
	require.NoError(t, err)
	return token
}

// TestAgentLabelsSurviveTheWire pins the round trip the admin API makes on
// every read and write: the object's labels are the record's labels, spelled
// the same way on both sides of the proto boundary.
func TestAgentLabelsSurviveTheWire(t *testing.T) {
	labels := map[string]string{
		"onex.example.com/onex": "",
		"tier":                  "prod",
	}

	model := conversion.AgentV1ToAgentModel(&v1.Agent{Name: "probe", Labels: labels})
	if model.Labels["tier"] != "prod" {
		t.Fatalf("the request lost its labels: %v", model.Labels)
	}
	if _, ok := model.Labels["onex.example.com/onex"]; !ok {
		t.Errorf("a label with an empty value was dropped: %v", model.Labels)
	}

	back := conversion.AgentModelToAgentV1(model)
	if len(back.GetLabels()) != len(labels) {
		t.Fatalf("Labels = %v, want %v", back.GetLabels(), labels)
	}
	for key, value := range labels {
		if back.GetLabels()[key] != value {
			t.Errorf("Labels[%q] = %q, want %q", key, back.GetLabels()[key], value)
		}
	}
}
