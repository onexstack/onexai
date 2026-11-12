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

package bearer

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "bearer-test-secret"

// newRouter returns a router with the given middlewares and a terminal handler
// that reports what the middleware left in the context.
func newRouter(middlewares ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/x", append(middlewares, func(c *gin.Context) {
		claims, ok := ClaimsFrom(c)
		if !ok {
			c.JSON(http.StatusOK, gin.H{"claims": nil})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"user_id":  claims.UserID,
			"name":     claims.Name,
			"is_sre":   claims.IsSRE,
			"is_admin": claims.IsAdmin,
		})
	})...)
	return engine
}

func get(engine *gin.Engine, token string, query bool) *httptest.ResponseRecorder {
	target := "/x"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		if query {
			req = httptest.NewRequest(http.MethodGet, target+"?token="+token, nil)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func mint(t *testing.T, claims Claims) string {
	t.Helper()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	require.NoError(t, err)
	return token
}

// TestAuthWithModeDeniesEverythingWhenToldTo pins the trap the config comment
// warns about: AlwaysDeny is not "refuse the invalid tokens", it is "refuse",
// and a valid token changes nothing. A deployment that sets it to be stricter
// has instead turned every authenticated route off.
func TestAuthWithModeDeniesEverythingWhenToldTo(t *testing.T) {
	engine := newRouter(AuthWithMode(testSecret, false, "AlwaysDeny"))
	valid := mint(t, Claims{UserID: "u1", IsAdmin: true})

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no token": get(engine, "", false),
		"valid":    get(engine, valid, false),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}

func TestAuthWithModeValidatesNormally(t *testing.T) {
	engine := newRouter(AuthWithMode(testSecret, false, ""))

	t.Run("no token", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, get(engine, "", false).Code)
	})

	t.Run("valid token", func(t *testing.T) {
		rec := get(engine, mint(t, Claims{UserID: "u1", Name: "Ada", IsSRE: true, IsAdmin: true}), false)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"user_id":"u1","name":"Ada","is_sre":true,"is_admin":true}`, rec.Body.String())
	})

	t.Run("token in the query string", func(t *testing.T) {
		// The WebSocket upgrade cannot set headers, which is why the query form
		// exists at all.
		rec := get(engine, mint(t, Claims{UserID: "u1"}), true)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("signed with another key", func(t *testing.T) {
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{UserID: "u1"}).
			SignedString([]byte("some-other-key"))
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, get(engine, token, false).Code)
	})

	t.Run("expired", func(t *testing.T) {
		claims := Claims{UserID: "u1"}
		claims.RegisteredClaims = jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, get(engine, token, false).Code)
	})

	t.Run("unsigned", func(t *testing.T) {
		// A token that asks to be trusted by algorithm rather than by signature is
		// the classic forgery; the keyfunc refuses anything that is not HMAC.
		token, err := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{UserID: "u1"}).
			SignedString(jwt.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, get(engine, token, false).Code)
	})
}

// TestRequireAdmin pins the three answers that matter: a non-administrator is
// refused, an administrator is let through, and a request that arrives with no
// claims is refused as a wiring fault rather than allowed.
//
// The last case is the one that decides whether a route mounted without
// AuthWithMode is open to everyone, so it is asserted as its own case rather
// than left to the end-to-end test — there, a missing gate looks the same as a
// handler that failed.
func TestRequireAdmin(t *testing.T) {
	adminOnly := newRouter(AuthWithMode(testSecret, false, ""), RequireAdmin())

	t.Run("no claims", func(t *testing.T) {
		// RequireAdmin alone: nothing put claims in the context.
		engine := newRouter(RequireAdmin())
		assert.Equal(t, http.StatusInternalServerError, get(engine, "", false).Code,
			"a request with no claims must not be let through")
	})

	t.Run("not an administrator", func(t *testing.T) {
		rec := get(adminOnly, mint(t, Claims{UserID: "u1", IsSRE: true}), false)
		assert.Equal(t, http.StatusForbidden, rec.Code,
			"an SRE is not an administrator: the two grants are independent")
	})

	t.Run("administrator", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, get(adminOnly, mint(t, Claims{UserID: "u1", IsAdmin: true}), false).Code)
	})
}
