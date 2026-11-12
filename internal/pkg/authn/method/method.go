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

// Package method records which authentication backend authenticated a request.
//
// Authentication happens before the target Agent is known, while an Agent's
// authnWebhooks are a per-Agent decision. Writing the winning backend into the
// request context is what lets the run admission check, later and in another
// package, answer "was this Agent's own webhook actually used?" without
// re-deriving it from the credentials.
//
// It lives in its own package because both sides of the boundary need it: the
// bearer middleware (internal/pkg/authn/bearer) and the agent-service
// middleware (internal/agent/pkg/authn). Importing either from the other would
// create a cycle.
package method

import "context"

// The built-in backends. A Webhook-authenticated request uses WebhookPrefix +
// the Webhook's name, so the Agent's list can be compared by name.
const (
	// AKSK is a request signed with an access key / secret key pair.
	AKSK = "aksk"
	// JWT is the platform's HS256 browser token.
	JWT = "jwt"
	// OIDC is a token accepted by the Kubernetes authenticator chain.
	OIDC = "oidc"
	// Anonymous is the anonymous identity a deployment may enable.
	Anonymous = "anonymous"
	// Bypass is the development switch that admits an unsigned request.
	Bypass = "bypass"
	// AlwaysAllow is the development switch that admits every request as admin.
	AlwaysAllow = "always-allow"
	// WebhookPrefix marks a request authenticated by an Agent-level TokenReview
	// Webhook. The rest of the string is the Webhook's name.
	WebhookPrefix = "webhook:"
)

type contextKey struct{}

// With returns ctx carrying the authentication method.
func With(ctx context.Context, name string) context.Context {
	if name == "" {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, name)
}

// From returns the authentication method recorded on ctx.
func From(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	name, ok := ctx.Value(contextKey{}).(string)
	return name, ok && name != ""
}

// IsWebhook reports whether the method names an Agent-level Webhook.
func IsWebhook(name string) bool {
	return len(name) > len(WebhookPrefix) && name[:len(WebhookPrefix)] == WebhookPrefix
}

// WebhookName returns the Webhook name carried by a webhook method.
func WebhookName(name string) string {
	if !IsWebhook(name) {
		return ""
	}
	return name[len(WebhookPrefix):]
}
