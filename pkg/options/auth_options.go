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

package options

import (
	"fmt"

	"github.com/spf13/pflag"
)

// Authorization modes understood by the JWT middleware.
const (
	// AuthorizationModeAlwaysAllow injects an admin identity and skips every
	// auth check. It is refused at startup (see Validate) and kept only so the
	// flag's accepted values can be named.
	AuthorizationModeAlwaysAllow = "AlwaysAllow"
	// AuthorizationModeAlwaysDeny blocks every authenticated endpoint with 403.
	AuthorizationModeAlwaysDeny = "AlwaysDeny"
)

// AuthOptions holds authentication and authorization configuration.
// Mirrors Kubernetes-style --anonymous-auth and --authorization-mode flags.
type AuthOptions struct {
	// AnonymousAuth allows unauthenticated requests with an anonymous identity.
	//
	// It is refused at startup: it grants a usable identity on every
	// authenticated endpoint to a caller who presents no credential at all.
	AnonymousAuth bool `json:"anonymous-auth" mapstructure:"anonymous-auth"`
	// AuthorizationMode controls how endpoints are protected:
	//   "" (empty)       — standard JWT validation (default)
	//   "AlwaysDeny"     — block all requests with 403
	//
	// "AlwaysAllow" (inject admin identity, skip all auth checks) is refused at
	// startup and is not a supported configuration.
	AuthorizationMode string `json:"authorization-mode" mapstructure:"authorization-mode"`

	// BypassAKSK disables inbound AK/SK signature verification on /v1. False by
	// default: every path is signed from the moment authz lands. Setting this
	// true is a local-development affordance for running the server without a
	// signing caller (e.g. oai-chat-apiserver) in front of it.
	BypassAKSK bool `json:"bypass-aksk" mapstructure:"bypass-aksk"`

	// ControlToken is a second gate on the two resources that decide who may
	// use everything else: /v1/core/secrets and /v1/agent/authorizationrules. Any signed key can
	// reach the run surface, but only a caller that also presents
	// X-Onex-Control-Token may rewrite credentials or policy.
	//
	// Empty means "no second gate" — the behaviour every deployment had before
	// this option existed. It is set together on both sides: OneX's oaiagent
	// client sends the header, onexai checks it. A one-sided rollout turns
	// provisioning into a 403, so the committed configs leave both empty and the
	// deployment supplies the pair from the same Secret.
	ControlToken string `json:"control-token" mapstructure:"control-token"`

	// WebhookToken is the shared Bearer credential a Kubernetes-style webhook
	// caller presents to the TokenReview/SubjectAccessReview endpoints. It is a
	// credential between control-plane components, not a user credential.
	//
	// Empty disables the native review endpoints: they answer 503. Exposing a
	// token-verification and policy-introspection surface without a credential is
	// not a sane default, so an operator must opt in explicitly.
	WebhookToken string `json:"webhook-token" mapstructure:"webhook-token"`

	// AuthenticationConfig is the path to an apiserver.config.k8s.io/v1
	// AuthenticationConfiguration. It adds Kubernetes-native OIDC JWT
	// authenticators ahead of the platform's HS256 token; empty keeps the
	// existing credential path only.
	AuthenticationConfig string `json:"authentication-config" mapstructure:"authentication-config"`
}

// NewAuthOptions creates a AuthOptions with default values.
func NewAuthOptions() *AuthOptions {
	return &AuthOptions{}
}

// Validate checks the AuthOptions fields.
//
// The two permissive settings are refused rather than warned about: by the time
// a request arrives, a server that admits anonymous or every caller looks
// exactly like a working deployment, so the misconfiguration has to stop the
// process instead of logging. AlwaysAllow injects an admin identity that no
// credential backs; anonymous-auth hands the same identity-bearing middleware an
// unauthenticated caller.
func (o *AuthOptions) Validate() []error {
	var errs []error

	if o.AnonymousAuth {
		errs = append(errs, fmt.Errorf(
			"auth.anonymous-auth is not supported: an unauthenticated caller would receive an identity"))
	}
	if o.AuthorizationMode == AuthorizationModeAlwaysAllow {
		errs = append(errs, fmt.Errorf(
			"auth.authorization-mode AlwaysAllow is not supported: it grants every caller an admin identity"))
	}

	return errs
}

// AddFlags adds auth-related flags to the given FlagSet.
func (o *AuthOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.BoolVar(&o.AnonymousAuth, fullPrefix+".anonymous-auth", o.AnonymousAuth,
		"Allow unauthenticated requests with an anonymous identity.")
	fs.StringVar(&o.AuthorizationMode, fullPrefix+".authorization-mode", o.AuthorizationMode,
		"Authorization mode: AlwaysDeny, or empty for normal JWT auth (AlwaysAllow is refused at startup).")
	fs.BoolVar(&o.BypassAKSK, fullPrefix+".bypass-aksk", o.BypassAKSK,
		"Skip inbound AK/SK signature verification on /v1 (local development only).")
	fs.StringVar(&o.ControlToken, fullPrefix+".control-token", o.ControlToken,
		"Second gate for /v1/core/secrets and /v1/agent/authorizationrules (X-Onex-Control-Token); empty disables it.")
	fs.StringVar(&o.WebhookToken, fullPrefix+".webhook-token", o.WebhookToken,
		"Bearer credential for the Kubernetes TokenReview/SubjectAccessReview endpoints; empty disables them (503).")
	fs.StringVar(&o.AuthenticationConfig, fullPrefix+".authentication-config", o.AuthenticationConfig,
		"Path to an apiserver.config.k8s.io/v1 AuthenticationConfiguration; empty keeps HS256-only authentication.")
}
