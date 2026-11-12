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

// Package k8s adapts Kubernetes authentication and authorization interfaces to
// the Gin surface the platform already runs. It is deliberately thin: the
// parsing, OIDC discovery, webhook calls and caching all stay in
// k8s.io/apiserver; this package only moves an identity or an AttributesRecord
// across the boundary.
package k8s

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/authn/method"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// Middleware authenticates a request with a Kubernetes authenticator.Request and
// stores both the platform Claims and the Kubernetes user.Info.
//
// Storing Claims is what lets the existing handlers keep reading
// bearer.ClaimsFrom; storing user.Info is what lets the authorization adapter
// answer with the groups and extra fields the authenticator produced. One
// request carries one identity; neither representation is derived a second time.
func Middleware(authn authenticator.Request) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authn == nil {
			core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("authentication is not configured"))
			c.Abort()
			return
		}

		resp, ok, err := authn.AuthenticateRequest(c.Request)
		if err != nil {
			core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage("authentication failed: %s", err.Error()))
			c.Abort()
			return
		}
		if !ok || resp == nil || resp.User == nil {
			core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage("missing or invalid credential"))
			c.Abort()
			return
		}

		bearer.SetClaims(c, ClaimsFromUser(resp.User))
		ctx := method.With(WithUser(c.Request.Context(), resp.User), method.OIDC)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// ClaimsFromUser maps a Kubernetes identity to the platform's claims.
//
// The mapping is intentionally group-based: the authenticator's claim mappings
// are where an OIDC issuer's claims become groups, and those same groups are
// what an administrator or SRE grant already means in this platform. Extra
// fields carry the display attributes a handler may show.
func ClaimsFromUser(info user.Info) *bearer.Claims {
	if info == nil {
		return nil
	}
	claims := &bearer.Claims{
		UserID:        info.GetName(),
		Name:          FirstExtra(info, "onexai.io/name"),
		Provider:      FirstExtra(info, "onexai.io/provider"),
		OpenID:        FirstExtra(info, "onexai.io/open-id"),
		AccountRole:   FirstExtra(info, "onexai.io/account-role"),
		ExternalAdmin: TruthyExtra(info, "onexai.io/external-admin"),
		ExternalSRE:   TruthyExtra(info, "onexai.io/external-sre"),
	}
	claims.IsAdmin = HasGroup(info, "system:masters") || HasGroup(info, "onexai:admin")
	claims.IsSRE = HasGroup(info, "onexai:sre")
	return claims
}

// HasGroup reports whether info belongs to group.
func HasGroup(info user.Info, group string) bool {
	if info == nil {
		return false
	}
	for _, g := range info.GetGroups() {
		if g == group {
			return true
		}
	}
	return false
}

// FirstExtra returns the first value of key, or "" when absent.
func FirstExtra(info user.Info, key string) string {
	if info == nil {
		return ""
	}
	values := info.GetExtra()[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// TruthyExtra reports whether key carries a boolean-looking true.
func TruthyExtra(info user.Info, key string) bool {
	switch FirstExtra(info, key) {
	case "true", "True", "TRUE", "1":
		return true
	default:
		return false
	}
}

type userContextKey struct{}

// WithUser attaches the Kubernetes identity to ctx.
func WithUser(ctx context.Context, info user.Info) context.Context {
	return context.WithValue(ctx, userContextKey{}, info)
}

// UserFromContext retrieves the identity attached by Middleware.
func UserFromContext(ctx context.Context) (user.Info, bool) {
	info, ok := ctx.Value(userContextKey{}).(user.Info)
	return info, ok
}
