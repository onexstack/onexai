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
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"

	chatbiz "github.com/onexstack/onexai/internal/chat/biz"
	userbiz "github.com/onexstack/onexai/internal/chat/biz/v1/user"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

const statusTestSecret = "status-test-secret"

type statusUserBiz struct {
	userbiz.UserBiz
	active bool
	role   string
}

func (f *statusUserBiz) AccountStatus(context.Context, string) (bool, bool, string, error) {
	return true, f.active, f.role, nil
}

type statusIBiz struct {
	chatbiz.IBiz
	user *statusUserBiz
}

func (f *statusIBiz) UserV1() userbiz.UserBiz { return f.user }

func statusToken(t *testing.T, claims bearer.Claims) string {
	t.Helper()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(statusTestSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return token
}

func clearStatusCache(h *Handler) {
	h.statusMu.Lock()
	h.statusCache = make(map[string]accountStatusEntry)
	h.statusMu.Unlock()
}

func statusRoles(t *testing.T, h *Handler, token string) (bool, bool, int) {
	t.Helper()
	engine := gin.New()
	engine.GET("/x", h.AuthMiddleware(), func(c *gin.Context) {
		claims, _ := bearer.ClaimsFrom(c)
		c.JSON(http.StatusOK, gin.H{"admin": claims.IsAdmin, "sre": claims.IsSRE})
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return false, false, rec.Code
	}
	var out struct {
		Admin bool `json:"admin"`
		SRE   bool `json:"sre"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return out.Admin, out.SRE, rec.Code
}

// TestAuthMiddlewareRecomputesAccountRole pins the two directions a role change
// must take effect in on an already-issued token: a demoted administrator loses
// admin, and a promoted SRE gains it. It also pins that the external (control
// plane) half survives the recomputation.
func TestAuthMiddlewareRecomputesAccountRole(t *testing.T) {
	userBiz := &statusUserBiz{active: true, role: ""}
	h := &Handler{biz: &statusIBiz{user: userBiz}, JWTSecret: statusTestSecret}

	// The token was issued while the account was an administrator.
	token := statusToken(t, bearer.Claims{
		UserID: "u1", Provider: "local", AccountRole: "admin", IsAdmin: true,
	})

	admin, sre, code := statusRoles(t, h, token)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if admin || sre {
		t.Errorf("demoted account still has roles: admin=%v sre=%v", admin, sre)
	}

	// The status cache is intentional (a database read per request would be
	// worse); a role change propagates within its TTL. Clear it here to assert
	// the recomputation itself rather than the cache's lifetime.
	clearStatusCache(h)
	userBiz.role = "sre"
	admin, sre, code = statusRoles(t, h, token)
	if code != http.StatusOK || !admin || !sre {
		t.Errorf("role=sre: admin=%v sre=%v code=%d, want both", admin, sre, code)
	}

	// An external grant is not the account's to lose.
	userBiz.role = ""
	external := statusToken(t, bearer.Claims{
		UserID: "u2", Provider: "local", ExternalAdmin: true, IsAdmin: true,
	})
	admin, _, code = statusRoles(t, h, external)
	if code != http.StatusOK || !admin {
		t.Errorf("external admin lost the grant: admin=%v code=%d", admin, code)
	}
}

func TestAuthMiddlewareRefusesDisabledAccount(t *testing.T) {
	userBiz := &statusUserBiz{active: false}
	h := &Handler{biz: &statusIBiz{user: userBiz}, JWTSecret: statusTestSecret}
	token := statusToken(t, bearer.Claims{UserID: "u1", Provider: "local"})

	engine := gin.New()
	engine.GET("/x", h.AuthMiddleware(), func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
