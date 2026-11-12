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

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// oncallInternalSecret is the shared credential the tests below present. Not a
// credential: it lives in this file and opens nothing outside the test.
const oncallInternalSecret = "oncall-internal-test-secret"

// TestOncallReadSurfaceIsNotReachableWithAUserToken is the property that
// matters most about these two routes, and the one nothing else would catch.
//
// They read tickets out of every duty group they are asked about, with no role
// check of their own — the gate is the shared internal credential, and the
// sibling /v1/tickets surface is gated by an admin claim instead. Mounting them
// in the wrong group would leave them answering any learner who has a token,
// and nothing would fail: the responses would just be correct, and wrong to
// send.
func TestOncallReadSurfaceIsNotReachableWithAUserToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(gin.Recovery())
	h := &Handler{
		JWTSecret: ticketTestSecret,
		InternalCredentials: []customoptions.InternalCredential{
			{Subject: "ox-jobserver", Secret: oncallInternalSecret},
		},
	}
	h.ApplyTo(engine.Group("/v1"))

	// A gate test on a route nobody mounted passes while asserting nothing.
	require.True(t, routeExists(engine, http.MethodGet, "/v1/internal/oncall/tickets"),
		"GET /v1/internal/oncall/tickets is not registered")
	require.True(t, routeExists(engine, http.MethodGet, "/v1/internal/oncall/tickets/:ticketID"),
		"the single-ticket route is not registered")

	// The conversation route is deliberately **absent**, and asserting its absence
	// is the point: the write-up is produced when the ticket closes, so the
	// consumer downstream reads a summary rather than a transcript. A route left
	// behind would be an unguarded read of group conversations that nobody calls —
	// which is exactly the kind of thing that is found months later.
	assert.False(t, routeExists(engine, http.MethodGet, "/v1/internal/oncall/tickets/:ticketID/transcript"),
		"the transcript route should be gone: the write-up is generated here now")

	member := ticketToken(t, bearer.Claims{UserID: "u1", Provider: "local"})

	t.Run("no credential at all", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, oncallGet(engine, "", "").Code)
	})

	t.Run("a learner's bearer token is not a credential", func(t *testing.T) {
		rec := oncallGet(engine, member, "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code,
			"a user token must not open the service-to-service read surface")
	})

	t.Run("a wrong shared secret is refused", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, oncallGet(engine, "", "not-the-secret").Code)
	})

	t.Run("the right shared secret gets through", func(t *testing.T) {
		// The handler has no dependencies in this test, so what it answers past
		// the gate is a 500 from recovery. What is asserted is only that the
		// gate let it through: a 401 or 403 here would mean the credential was
		// not accepted.
		rec := oncallGet(engine, "", oncallInternalSecret)
		assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "the credential was refused")
		assert.NotEqual(t, http.StatusForbidden, rec.Code, "the credential was refused")
	})
}

// TestOncallReadSurfaceIsClosedWhenNothingIsConfigured pins the deployment with
// no integrator.
//
// An empty credential list is read as "these endpoints are closed", never as
// "no authentication required" — the latter would open them on exactly the
// deployments that never meant to expose them.
//
// The refusal is a 403 that names the missing configuration rather than the 401
// a bad secret gets, and the difference is the whole point of asserting the
// message: "you sent the wrong secret" sends an operator to rotate a
// credential, while "this deployment has no integrator configured" sends them
// to the config file. The same distinction the knowledge-base reader draws
// between 403 and 503.
func TestOncallReadSurfaceIsClosedWhenNothingIsConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(gin.Recovery())
	h := &Handler{JWTSecret: ticketTestSecret}
	h.ApplyTo(engine.Group("/v1"))

	rec := oncallGet(engine, "", oncallInternalSecret)
	assert.Equal(t, http.StatusForbidden, rec.Code,
		"an unconfigured deployment must refuse every caller, including one holding a plausible secret")
	assert.Contains(t, rec.Body.String(), "not configured",
		"the refusal must name the missing configuration, not read as a wrong credential")
}

func oncallGet(engine *gin.Engine, token, internalToken string, paths ...string) *httptest.ResponseRecorder {
	path := "/v1/internal/oncall/tickets"
	if len(paths) > 0 {
		path = paths[0]
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if internalToken != "" {
		req.Header.Set("X-Internal-Token", internalToken)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}
