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

package authn

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func runControl(t *testing.T, required, path, presented string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ControlToken(required))
	r.POST("/*any", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodPost, path, nil)
	if presented != "" {
		req.Header.Set(ControlTokenHeader, presented)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// TestControlTokenGuardsOnlyTheControlPlane pins the scope: the run surface is
// reachable with the signed key alone, and only the two resources that can mint
// credentials or rewrite policy need the second one.
func TestControlTokenGuardsOnlyTheControlPlane(t *testing.T) {
	const token = "s3cret"

	assert.Equal(t, http.StatusForbidden, runControl(t, token, "/v1/core/secrets", ""))
	assert.Equal(t, http.StatusNoContent, runControl(t, token, "/v1/core/secrets", token))
	assert.Equal(t, http.StatusForbidden, runControl(t, token, "/v1/agent/authorizationrules/onex-u-1", "wrong"))
	assert.Equal(t, http.StatusNoContent, runControl(t, token, "/v1/agent/authorizationrules", token))

	// A signed run request passes without the second credential.
	assert.Equal(t, http.StatusNoContent, runControl(t, token, "/v1/agent/agents", ""))
	assert.Equal(t, http.StatusNoContent, runControl(t, token, "/v1/sessions", ""))

	// The prefix match is on a segment boundary: a resource whose name merely
	// starts with "secrets" is not the secret resource.
	assert.Equal(t, http.StatusNoContent, runControl(t, token, "/v1/core/secretsomething", ""))
}

// TestControlTokenEmptyLeavesTheSurfaceAsItWas keeps the option opt-in: a
// deployment that does not set it behaves exactly as before.
func TestControlTokenEmptyLeavesTheSurfaceAsItWas(t *testing.T) {
	assert.Equal(t, http.StatusNoContent, runControl(t, "", "/v1/core/secrets", ""))
}
