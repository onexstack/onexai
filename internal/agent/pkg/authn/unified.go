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
	"github.com/gin-gonic/gin"
	"k8s.io/apiserver/pkg/authentication/authenticator"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	k8sauthn "github.com/onexstack/onexai/internal/pkg/authn/k8s"
	"github.com/onexstack/onexai/internal/pkg/authn/method"
)

// UnifiedAuthConfig configures the either-credential middleware.
type UnifiedAuthConfig struct {
	// Provider resolves an AK/SK key id to its secret.
	Provider SecretProviderFunc
	// BypassAKSK allows an unsigned service call when no credential is present.
	// It is the development switch the signed /v1 face already exposes.
	BypassAKSK bool
	// Nonces holds the AK/SK replay guard.
	Nonces NonceStore
	// JWTSecret validates the browser bearer token.
	JWTSecret string
	// AnonymousAuth mirrors the bearer middleware's anonymous mode. The user
	// guard in the handler layer refuses writes from an anonymous caller, so
	// turning it on exposes reads only.
	AnonymousAuth bool
	// AuthzMode is the bearer middleware's AlwaysAllow/AlwaysDeny switch.
	AuthzMode string
	// TokenAuthenticator adds the Kubernetes OIDC path ahead of the platform's
	// HS256 token. It is nil when auth.authentication-config is empty, in which
	// case behaviour is exactly the pre-OIDC one.
	TokenAuthenticator authenticator.Request
	// ControlToken gates the two resources that can rewrite every permission.
	// It applies to the AK/SK path only: a browser administrator's signed claim
	// is the stronger credential, which is the same contract /v1/admin has.
	ControlToken string
}

// UnifiedAuth accepts either an AK/SK signed service call or a browser bearer
// token, so one resource URL serves both callers.
//
// Selection is by presence, never by fallback:
//
//   - Any AK/SK header present selects the signed path. A partial header set is
//     an attempted signed request and is refused; it never downgrades to JWT.
//   - Otherwise a bearer token selects the JWT path. It authenticates **any
//     signed-in user**, not only an administrator: this surface also serves the
//     console's ordinary users. Which resources a non-administrator may touch
//     is decided after authentication, by the handler layer's user guard (see
//     handler.Handler.UserGuard), which knows the resources and their owners.
//     Keeping the role check here would make route-level policy impossible to
//     express: one group carries both the platform catalog and the governance
//     resources that stay administrator-only.
//   - With no credential at all, BypassAKSK allows the call; without the bypass
//     the bearer middleware answers 401 (or anonymous, which the user guard
//     then refuses when the method writes).
//
// A valid JWT alongside failing AK/SK headers does not rescue the request:
// allowing that would make credential confusion a way around the stronger check.
func UnifiedAuth(cfg UnifiedAuthConfig) gin.HandlerFunc {
	nonces := normalizeNonces(cfg.Nonces)

	return func(c *gin.Context) {
		switch {
		case hasAKSKHeaders(c):
			if !cfg.BypassAKSK && !AuthenticateAKSK(c, cfg.Provider, nonces) {
				return
			}
			if !checkControlToken(c, cfg.ControlToken) {
				return
			}
		case bearer.HasToken(c):
			// The Kubernetes OIDC authenticator goes first: it is the path a
			// deployment moves its machine-to-machine callers onto. A token it
			// does not recognise falls through to the platform's HS256 token, so
			// an existing browser session keeps working during the transition.
			if cfg.TokenAuthenticator != nil {
				if resp, ok, err := cfg.TokenAuthenticator.AuthenticateRequest(c.Request); err == nil && ok && resp != nil && resp.User != nil {
					bearer.SetClaims(c, k8sauthn.ClaimsFromUser(resp.User))
					c.Request = c.Request.WithContext(method.With(c.Request.Context(), method.OIDC))
					break
				}
			}
			if !bearer.Authenticate(c, cfg.JWTSecret, cfg.AnonymousAuth, cfg.AuthzMode) {
				return
			}
		case cfg.BypassAKSK:
			// An unsigned service call is the documented development bypass.
			c.Request = c.Request.WithContext(method.With(c.Request.Context(), method.Bypass))
		default:
			if !bearer.Authenticate(c, cfg.JWTSecret, cfg.AnonymousAuth, cfg.AuthzMode) {
				return
			}
		}

		c.Next()
	}
}
