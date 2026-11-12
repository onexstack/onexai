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

package k8s

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

func TestClaimsFromUser(t *testing.T) {
	claims := ClaimsFromUser(&user.DefaultInfo{
		Name:   "go.kong",
		Groups: []string{"system:authenticated", "system:masters", "onexai:sre"},
		Extra: map[string][]string{
			"onexai.io/name":           {"Kong"},
			"onexai.io/provider":       {"feishu"},
			"onexai.io/external-admin": {"true"},
		},
	})

	require.NotNil(t, claims)
	assert.Equal(t, "go.kong", claims.UserID)
	assert.Equal(t, "Kong", claims.Name)
	assert.Equal(t, "feishu", claims.Provider)
	assert.True(t, claims.IsAdmin)
	assert.True(t, claims.IsSRE)
	assert.True(t, claims.ExternalAdmin)
	assert.False(t, claims.ExternalSRE)
}

func TestClaimsFromUserUsesCustomRoleGroups(t *testing.T) {
	claims := ClaimsFromUser(&user.DefaultInfo{
		Name:   "sre-1",
		Groups: []string{"system:authenticated", "onexai:sre"},
	})
	require.NotNil(t, claims)
	assert.False(t, claims.IsAdmin)
	assert.True(t, claims.IsSRE)
}

func middlewareEngine(authn authenticator.Request) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Middleware(authn))
	engine.GET("/", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	return engine
}

func TestMiddlewareRejectsWhenUnconfigured(t *testing.T) {
	engine := middlewareEngine(nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestMiddlewareRejectsAnUnknownCredential(t *testing.T) {
	engine := middlewareEngine(authenticator.RequestFunc(
		func(*http.Request) (*authenticator.Response, bool, error) { return nil, false, nil },
	))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddlewareStoresTheClaims(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var got *bearer.Claims
	engine := gin.New()
	engine.Use(Middleware(authenticator.RequestFunc(
		func(*http.Request) (*authenticator.Response, bool, error) {
			return &authenticator.Response{User: &user.DefaultInfo{
				Name:   "go.kong",
				Groups: []string{"onexai:admin"},
			}}, true, nil
		},
	)))
	engine.GET("/", func(c *gin.Context) {
		got, _ = bearer.ClaimsFrom(c)
		c.Status(http.StatusNoContent)
	})

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusNoContent, w.Code)
	require.NotNil(t, got)
	assert.Equal(t, "go.kong", got.UserID)
	assert.True(t, got.IsAdmin)
}
