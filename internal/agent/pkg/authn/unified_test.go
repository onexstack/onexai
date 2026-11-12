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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

const unifiedJWTSecret = "jwt-secret"

func unifiedEngine(t *testing.T, cfg UnifiedAuthConfig) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if cfg.Provider == nil {
		cfg.Provider = testProvider
	}
	if cfg.Nonces == nil {
		cfg.Nonces = newMemoryNonceStore()
	}
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = unifiedJWTSecret
	}
	engine := gin.New()
	engine.Use(UnifiedAuth(cfg))
	engine.POST("/*any", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	engine.GET("/*any", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return engine
}

func adminToken(t *testing.T, admin bool) string {
	t.Helper()
	claims := bearer.Claims{
		UserID:   "user-1",
		Provider: "test",
		IsAdmin:  admin,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(unifiedJWTSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}

func signedHeaders(t *testing.T, method, path string) map[string]string {
	t.Helper()
	return sign(t, method, path, nil, time.Now())
}

func performUnified(t *testing.T, engine *gin.Engine, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestUnifiedAuthCredentialSelection(t *testing.T) {
	const path = "/v1/agent/skills"

	t.Run("valid AK/SK", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{})
		rec := performUnified(t, engine, http.MethodPost, path, signedHeaders(t, http.MethodPost, path))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("admin JWT", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{})
		rec := performUnified(t, engine, http.MethodGet, path, map[string]string{
			"Authorization": "Bearer " + adminToken(t, true),
		})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-admin JWT is authenticated", func(t *testing.T) {
		// Role policy is the handler-layer user guard's job now; this
		// middleware only answers "who is this". A refusal here would make it
		// impossible to open the shared catalog to ordinary users.
		engine := unifiedEngine(t, UnifiedAuthConfig{})
		rec := performUnified(t, engine, http.MethodGet, path, map[string]string{
			"Authorization": "Bearer " + adminToken(t, false),
		})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("no credential", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{})
		rec := performUnified(t, engine, http.MethodGet, path, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("failed AK/SK is not rescued by a valid JWT", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{})
		headers := signedHeaders(t, http.MethodPost, path)
		headers[headerSign] = "00"
		headers["Authorization"] = "Bearer " + adminToken(t, true)
		rec := performUnified(t, engine, http.MethodPost, path, headers)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401: AK/SK presence must pin the credential type", rec.Code)
		}
	})

	t.Run("partial AK/SK headers do not downgrade to JWT", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{})
		rec := performUnified(t, engine, http.MethodPost, path, map[string]string{
			headerAPIKey:    testAPIKey,
			"Authorization": "Bearer " + adminToken(t, true),
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}

func TestUnifiedAuthBypassAndModes(t *testing.T) {
	const path = "/v1/agent/skills"

	t.Run("bypass allows an unsigned call", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{BypassAKSK: true})
		rec := performUnified(t, engine, http.MethodGet, path, nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("bypass still validates a presented JWT", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{BypassAKSK: true})
		rec := performUnified(t, engine, http.MethodGet, path, map[string]string{
			"Authorization": "Bearer not-a-token",
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("anonymous auth is authenticated but not an administrator", func(t *testing.T) {
		// The handler-layer user guard refuses writes from an anonymous caller;
		// this middleware's answer is only the identity.
		engine := unifiedEngine(t, UnifiedAuthConfig{AnonymousAuth: true})
		rec := performUnified(t, engine, http.MethodGet, path, nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("AlwaysAllow opens the JWT path", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{AuthzMode: "AlwaysAllow"})
		rec := performUnified(t, engine, http.MethodGet, path, nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("AlwaysDeny refuses the JWT path", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{AuthzMode: "AlwaysDeny"})
		rec := performUnified(t, engine, http.MethodGet, path, map[string]string{
			"Authorization": "Bearer " + adminToken(t, true),
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
}

func TestUnifiedAuthControlTokenAppliesToTheSignedPathOnly(t *testing.T) {
	const path = "/v1/core/secrets"

	t.Run("AK/SK without the control token is refused", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{ControlToken: "control"})
		rec := performUnified(t, engine, http.MethodPost, path, signedHeaders(t, http.MethodPost, path))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("AK/SK with the control token passes", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{ControlToken: "control"})
		headers := signedHeaders(t, http.MethodPost, path)
		headers[ControlTokenHeader] = "control"
		rec := performUnified(t, engine, http.MethodPost, path, headers)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("an administrator does not need the second credential", func(t *testing.T) {
		engine := unifiedEngine(t, UnifiedAuthConfig{ControlToken: "control"})
		rec := performUnified(t, engine, http.MethodGet, path, map[string]string{
			"Authorization": "Bearer " + adminToken(t, true),
		})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
	})
}
