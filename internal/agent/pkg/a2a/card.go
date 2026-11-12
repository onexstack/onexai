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
// The original repo for this file is https://github.com/onexstack/onex.

package a2a

import (
	"context"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

const (
	// defaultDescription is what a deployment that configured none says.
	defaultDescription = "onexai AI agent"
	// defaultVersion is the version the card carries when the build injected
	// none, which is the case for `go run` and for tests.
	defaultVersion = "0.0.0"

	// akskScheme names the security scheme the endpoints actually enforce.
	akskScheme = "aksk"
	// akskExtensionURI describes a signature the OpenAPI security-scheme union
	// cannot express. A client that only sends X-API-KEY will be refused, so
	// the extension is how the surface says what else is required.
	akskExtensionURI = "https://onexai.onexstack.io/a2a/extensions/aksk-signature/v1"
	// agentSelectionExtensionURI declares the metadata convention that picks an
	// agent set per call.
	agentSelectionExtensionURI = "https://onexai.onexstack.io/a2a/extensions/agent-selection/v1"
)

// buildCard is the manifest this service publishes.
//
// It describes the endpoint, not the configured agents: a card is public, and
// listing an internal agent catalogue on it would publish the deployment's
// inventory to anyone who can reach the route. The agent for a call is chosen
// per request through metadata, which the selection extension documents.
func buildCard(opts Options) *a2a.AgentCard {
	description := opts.Description
	if description == "" {
		description = defaultDescription
	}
	version := opts.Version
	if version == "" {
		version = defaultVersion
	}
	// The base every interface URL is built from. The card's own location
	// (PathCard) is deliberately not used: the card is discovered at the origin
	// root, while the interfaces it advertises live under the transport mount.
	base := Path
	if opts.PublicURL != "" {
		base = strings.TrimSuffix(opts.PublicURL, "/")
	}

	selection := map[string]any{"keys": []string{"agents", "agent"}}
	if opts.DefaultAgent != "" {
		selection["default"] = opts.DefaultAgent
	}

	return &a2a.AgentCard{
		Name:        "onexai",
		Description: description,
		Version:     version,
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(base, a2a.TransportProtocolJSONRPC),
			a2a.NewAgentInterface(base+"/rest", a2a.TransportProtocolHTTPJSON),
		},
		// The inputs the surface actually accepts: text, and images by URL.
		// Anything else is refused rather than silently dropped.
		DefaultInputModes:  []string{"text/plain", "image/png", "image/jpeg", "image/webp"},
		DefaultOutputModes: []string{"text/plain"},
		// No skills, and explicitly an empty array rather than nil: the field
		// has no omitempty, so nil would serialize as `"skills": null`, which a
		// strict client refuses to decode.
		Skills: []a2a.AgentSkill{},
		Capabilities: a2a.AgentCapabilities{
			Streaming: true,
			Extensions: []a2a.AgentExtension{
				{
					URI:         akskExtensionURI,
					Description: "Every call must carry an AK/SK signature, not just the API key.",
					Params: map[string]any{
						"headers":      []string{"X-API-KEY", "X-TIMESTAMP", "X-NONCE", "X-SIGN"},
						"algorithm":    "HMAC-SHA256",
						"signedString": "METHOD\\nPATH\\nhex(md5(body))\\nX-TIMESTAMP\\nX-NONCE",
					},
				},
				{
					URI:         agentSelectionExtensionURI,
					Description: "Names the agent set this call runs with.",
					Params:      selection,
				},
				{
					URI:         UsageExtensionURI,
					Description: "Task-lifetime token usage on the terminal status.",
				},
				{
					URI:         HITLExtensionURI,
					Description: "Human approval and user-input pauses as input-required tasks.",
				},
				{
					URI: StructuredOutputExtensionURI,
					Description: "A schema-validated answer as an application/json artifact carrying " +
						"the digest of the contract it satisfies.",
					Params: map[string]any{"metadata": OutputSchemaSHA256Metadata},
				},
			},
		},
		// The card is public; the endpoints it points at are not. Declaring the
		// scheme here is what tells a client that fetching the card is not the
		// same as being allowed to call.
		SecuritySchemes: a2a.NamedSecuritySchemes{
			akskScheme: a2a.APIKeySecurityScheme{
				Location: a2a.APIKeySecuritySchemeLocationHeader,
				Name:     "X-API-KEY",
				Description: "AK/SK signature auth. The API key alone is not enough: " +
					"X-TIMESTAMP, X-NONCE and X-SIGN must accompany it (see the aksk extension).",
			},
		},
		SecurityRequirements: a2a.SecurityRequirementsOptions{
			{akskScheme: a2a.SecuritySchemeScopes{}},
		},
	}
}

// cardHandler serves the card.
//
// A configured public URL (or the default relative path) makes the card
// constant, so the SDK's static handler is used. Only a deployment that
// declared its proxy as trusted gets per-request URLs, and then the base is
// derived from headers the proxy rewrote — see Options.TrustedProxy for why
// that is not the default.
func cardHandler(card *a2a.AgentCard, opts Options) http.Handler {
	if opts.PublicURL != "" || !opts.TrustedProxy {
		return a2asrv.NewStaticAgentCardHandler(card)
	}

	producer := a2asrv.AgentCardProducerFn(func(ctx context.Context) (*a2a.AgentCard, error) {
		base, _ := ctx.Value(baseURLKey{}).(string)
		if base == "" {
			return card, nil
		}
		return withBaseURL(card, base), nil
	})
	inner := a2asrv.NewAgentCardHandler(producer)

	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if base := requestBaseURL(req); base != "" {
			req = req.WithContext(context.WithValue(req.Context(), baseURLKey{}, base))
		}
		inner.ServeHTTP(rw, req)
	})
}

// baseURLKey carries the per-request base URL to the card producer, which the
// SDK calls with a context but without the request.
type baseURLKey struct{}

// requestBaseURL derives the surface's absolute base URL from a request that
// reached this process through a trusted proxy.
func requestBaseURL(req *http.Request) string {
	scheme := firstToken(req.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		scheme = "http"
		if req.TLS != nil {
			scheme = "https"
		}
	}
	host := firstToken(req.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = req.Host
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host + Path
}

// withBaseURL returns a copy of the card pointing at base.
//
// A copy rather than a mutation: the card is shared by every request, and
// rewriting it in place would race between two requests from different hosts.
func withBaseURL(card *a2a.AgentCard, base string) *a2a.AgentCard {
	clone := *card
	clone.SupportedInterfaces = []*a2a.AgentInterface{
		a2a.NewAgentInterface(base, a2a.TransportProtocolJSONRPC),
		a2a.NewAgentInterface(base+"/rest", a2a.TransportProtocolHTTPJSON),
	}
	return &clone
}

// firstToken returns the first comma-separated value, which is what a chain of
// proxies leaves behind.
func firstToken(value string) string {
	if i := strings.IndexByte(value, ','); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}
