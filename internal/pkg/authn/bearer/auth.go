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

// Package bearer authenticates requests carrying the HS256 bearer token this
// platform issues at POST /v1/auth/login, and answers the one authorization
// question a browser-facing surface asks: is this caller an administrator?
//
// # Why it is not under a service
//
// The token is minted by oai-chat-apiserver and validated by more than one
// service — chat's own routes, and the agent service's management surface. This
// package began as internal/chat/pkg/middleware/auth.go. Leaving it there would
// have made the agent service depend on a chat-owned package, which is the wrong
// direction (the agent is not chat's downstream), and it is the kind of
// duplicate that drifts: two parsers, two claim structs, and an "authenticated
// here, 401 there" that only shows up in production.
//
// # What is still weak, and where that is fixed
//
// This package decides *who* the caller is from the token, and nothing about
// whether the token was earned: the login endpoint's providers do not verify
// credentials, so the identity is asserted, not proven. That is a property of
// the issuer, documented there (see biz/v1/auth). What this package must get
// right is the opposite direction — that a caller cannot *claim* a role it was
// not granted — which is why roles travel inside the signed token and are never
// read from the request.
package bearer

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/authn/method"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// Claims holds the custom claims this platform's tokens carry.
//
// IsSRE and IsAdmin are two independent grants, not two names for one thing:
// the command tree gates different commands on each (x-sre-only / x-admin-only),
// and collapsing them would make x-sre-only mean x-admin-only. A token may carry
// either, both, or neither.
type Claims struct {
	UserID string `json:"user_id"`
	// Name is the display name the login provider reported, so a surface that
	// has to name the caller in a record can name a person rather than an
	// identifier. It is empty on tokens minted before the claim existed, and
	// callers fall back to UserID.
	Name     string `json:"name,omitempty"`
	OpenID   string `json:"open_id,omitempty"`
	Provider string `json:"provider"`
	// AccountRole is the role stored on the platform account ("" / admin /
	// sre). It is carried separately from IsAdmin/IsSRE so the chat service can
	// recompute the account's contribution after an administrator changes it,
	// while ExternalAdmin/ExternalSRE preserve the control plane's grants that
	// the token was issued with.
	AccountRole string `json:"account_role,omitempty"`
	// ExternalAdmin and ExternalSRE record the role lists' answer at issue
	// time. A token whose account is later demoted must not lose an
	// administrator grant that came from RBAC rather than from the account.
	ExternalAdmin bool `json:"external_admin,omitempty"`
	ExternalSRE   bool `json:"external_sre,omitempty"`
	IsSRE         bool `json:"is_sre,omitempty"`
	// IsAdmin marks a platform administrator. It is set by the issuer from the
	// deployment's configured admin list, and it is what the management surface
	// requires. A token minted before this claim existed carries it as false,
	// which is the safe direction: unknown privileges are not granted.
	IsAdmin bool `json:"is_admin,omitempty"`
	jwt.RegisteredClaims
}

const claimsKey = "jwt_claims"

// AuthWithMode validates the Bearer JWT token in the Authorization header or the
// "token" query parameter (used by the WebSocket upgrade path).
//
// When anonymousAuth is true, requests without a valid token are not rejected;
// instead they are assigned a default anonymous identity (user_id="anonymous",
// Provider="anonymous"). This is the equivalent of Kubernetes
// --anonymous-auth=true, and the platform's option validation refuses to start
// with it on (pkg/options/auth_options.go) — a token-less caller that receives
// an identity is the one outcome an identity check must not produce.
//
// When authzMode is "AlwaysAllow", the middleware injects admin-level claims
// unconditionally and never blocks. Useful for local debugging, and likewise
// refused at startup by the option validation.
//
// When authzMode is "AlwaysDeny", **every** request is refused with 403 —
// including one carrying a perfectly valid token. The name suggests "deny what
// fails validation"; it does not mean that, and a deployment that sets it to
// "lock things down" locks them down completely. Any other value (including "")
// means standard token validation.
func AuthWithMode(secret string, anonymousAuth bool, authzMode string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !Authenticate(c, secret, anonymousAuth, authzMode) {
			return
		}
		c.Next()
	}
}

// Authenticate performs the mode switch, token parse and claim storage, and
// writes the failure response. It never advances the chain: callers decide
// whether to call c.Next(). The split exists so a composite middleware can
// combine a bearer credential with another type without nesting c.Next().
func Authenticate(c *gin.Context, secret string, anonymousAuth bool, authzMode string) bool {
	// AlwaysAllow: inject admin claims and let everything through.
	if authzMode == "AlwaysAllow" {
		c.Set(claimsKey, &Claims{
			UserID:   "admin",
			Provider: "always-allow",
			IsSRE:    true,
			IsAdmin:  true,
		})
		c.Request = c.Request.WithContext(method.With(c.Request.Context(), method.AlwaysAllow))
		return true
	}

	// AlwaysDeny: block every request.
	if authzMode == "AlwaysDeny" {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("authorization mode is AlwaysDeny"))
		c.Abort()
		return false
	}

	tokenStr := extractToken(c)

	// Anonymous auth: inject anonymous claims and proceed.
	if tokenStr == "" && anonymousAuth {
		c.Set(claimsKey, &Claims{
			UserID:   "anonymous",
			Provider: "anonymous",
		})
		c.Request = c.Request.WithContext(method.With(c.Request.Context(), method.Anonymous))
		return true
	}

	if tokenStr == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage("missing authorization token"))
		c.Abort()
		return false
	}

	claims, err := ParseToken(tokenStr, secret)
	if err != nil {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage("invalid or expired token"))
		c.Abort()
		return false
	}

	c.Set(claimsKey, claims)
	c.Request = c.Request.WithContext(method.With(c.Request.Context(), method.JWT))
	return true
}

// ParseToken validates tokenStr against the platform JWT secret and returns its
// claims. It is the one implementation of "what does this token mean", shared by
// the Gin middleware and the Kubernetes TokenReview endpoint: two parsers would
// drift into "authenticated here, rejected there".
func ParseToken(tokenStr, secret string) (*Claims, error) {
	if tokenStr == "" {
		return nil, jwt.ErrTokenMalformed
	}
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, jwt.ErrTokenInvalidClaims
	}
	return claims, nil
}

// Auth validates the Bearer JWT token. It is AuthWithMode(secret, false, "") and
// exists because most call sites want exactly that.
func Auth(secret string) gin.HandlerFunc {
	return AuthWithMode(secret, false, "")
}

// RequireAdmin refuses a request whose caller is not an administrator.
//
// It must be installed **after** AuthWithMode: that middleware is what puts the
// claims in the context, and gin runs a group's handlers in the order they were
// given.
//
// # Why it answers 500 when no claims are present
//
// That case means the route was mounted without AuthWithMode — a wiring bug, not
// a permission decision. Reporting it as 403 would file our bug under the
// caller's name and hide it in a log of refused requests; 500 says whose problem
// it is. The direction is the safe one either way: no claims, no access.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !CheckAdmin(c) {
			return
		}
		c.Next()
	}
}

// CheckAdmin answers the administrator question for an already-authenticated
// request, writing the same 500/403 responses as RequireAdmin. It never
// advances the chain.
func CheckAdmin(c *gin.Context) bool {
	claims, ok := ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrInternal.WithMessage(
			"the administrator check ran without an authenticated caller"))
		c.Abort()
		return false
	}
	if !claims.IsAdmin {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("administrator role required"))
		c.Abort()
		return false
	}
	return true
}

// SetClaims stores claims on a gin context. It exists for a middleware that
// authenticated the request through a different mechanism — a Kubernetes
// authenticator, for example — while the handlers that read claims keep working
// unchanged. The mapping from that mechanism's identity to Claims lives next to
// the authenticator, not here.
func SetClaims(c *gin.Context, claims *Claims) {
	if c == nil || claims == nil {
		return
	}
	c.Set(claimsKey, claims)
}

// ClaimsFrom retrieves the JWT claims stored by the Auth middleware.
func ClaimsFrom(c *gin.Context) (*Claims, bool) {
	v, exists := c.Get(claimsKey)
	if !exists {
		return nil, false
	}
	claims, ok := v.(*Claims)
	return claims, ok
}

// HasToken reports whether a bearer credential is present.
func HasToken(c *gin.Context) bool {
	return extractToken(c) != ""
}

// Token returns the bearer credential exactly as presented, without validating
// it. It exists for a middleware that hands the token to an external
// TokenReview webhook instead of verifying it locally: the point of the
// webhook is that the platform cannot decide what the token means.
func Token(c *gin.Context) string {
	return extractToken(c)
}

func extractToken(c *gin.Context) string {
	// Prefer Authorization header.
	h := c.GetHeader("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	// Fall back to query param (WebSocket upgrade doesn't send headers).
	return c.Query("token")
}
