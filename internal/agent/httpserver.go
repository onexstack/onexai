package agent

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/onexstack/onexstack/pkg/server"

	"github.com/onexstack/onexai/internal/agent/pkg/a2a"
	"github.com/onexstack/onexai/internal/agent/pkg/authn"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/httpserver"
	mw "github.com/onexstack/onexai/internal/pkg/middleware/gin"
	"github.com/onexstack/onexai/pkg/media"
)

// NewGinServer initializes and returns a new HTTP server based on Gin.
// It returns the server.Server interface to abstract implementation details.
func (c *ServerConfig) NewGinServer() (server.Server, error) {
	engine := httpserver.NewRouter(httpserver.Options{
		ServiceName:        serviceName,
		FeatureOptions:     c.FeatureOptions,
		ObservabilitySkips: observabilityRouteSkips,
	})

	c.InstallRESTAPI(engine)

	servers := []server.Server{
		server.NewHTTPServer(c.InsecureServingOptions, c.SecureServingOptions, engine),
	}
	if internal, ok := c.internalServer(); ok {
		servers = append(servers, internal)
	}
	if len(servers) == 1 {
		return servers[0], nil
	}
	return &multiServer{servers: servers}, nil
}

// internalServer builds the runtime-only listener. It is a separate server
// rather than a route on the public one: capability tokens are scoped to one
// run, and a cross-tenant surface reachable from the public port is one
// misconfiguration away from being a public one.
func (c *ServerConfig) internalServer() (server.Server, bool) {
	if c.Capability == nil {
		return nil, false
	}
	addr := strings.TrimSpace(c.AgentOptions.Runtime.InternalAddr)
	if strings.EqualFold(addr, "off") {
		return nil, false
	}
	if addr == "" {
		addr = "127.0.0.1:8091"
	}
	if c.InsecureServingOptions != nil && c.InsecureServingOptions.Addr == addr {
		// One listener cannot serve two route trees; refusing is better than
		// silently replacing one with the other.
		return nil, false
	}

	internal := gin.New()
	internal.Use(gin.Recovery())
	internal.Any("/v1/mcp", gin.WrapH(c.Capability))
	if c.TaskStore != nil {
		internal.POST("/v1/runtime/tasks", gin.WrapH(c.TaskStore))
		internal.GET("/v1/runtime/tasks", gin.WrapH(c.TaskStore))
		internal.PUT("/v1/runtime/tasks/:id", gin.WrapH(c.TaskStore))
		internal.GET("/v1/runtime/tasks/:id", gin.WrapH(c.TaskStore))
		internal.POST("/v1/runtime/tasks/:id/settle", gin.WrapH(c.TaskStore))
	}
	if c.TokenRefresh != nil {
		internal.POST("/v1/runtime/token/refresh", gin.WrapH(c.TokenRefresh))
	}
	internal.GET("/readyz", func(ctx *gin.Context) { ctx.String(http.StatusOK, "ok") })
	return server.NewHTTPServer(&genericoptions.InsecureServingOptions{Addr: addr}, nil, internal), true
}

// InstallRESTAPI registers all RESTful API routes to the engine.
func (c *ServerConfig) InstallRESTAPI(engine *gin.Engine) {
	// Unauthenticated: the probe and the scrape are made by the platform, not
	// by a signed caller, and putting a signature in front of them would only
	// make the deployment look unhealthy while saying nothing about who may
	// call the API.
	engine.GET("/healthz", c.Handler.Healthz)

	// Readiness is a separate answer from liveness: a database that is away must
	// take this pod out of the load balancer, not restart it. See
	// internal/pkg/readiness.
	engine.GET("/readyz", gin.WrapH(c.Readiness.Handler()))

	// Kubernetes review webhooks. They are mounted outside /v1 because their
	// caller is a Kubernetes control plane, not a platform user or a signed
	// service: it authenticates with the kubeconfig's bearer token
	// (auth.webhook-token). An unset token makes the group answer 503 rather than
	// exposing an unauthenticated token-verification or policy-introspection
	// endpoint.
	//
	// The paths are the ones client-go appends to the kubeconfig server URL, so
	// an external apiserver can point its token/SAR webhook config at this
	// process without a path rewrite.
	webhooks := engine.Group("/apis", authn.WebhookBearer(c.WebhookToken))
	webhooks.POST("/authentication.k8s.io/v1/tokenreviews", c.Handler.TokenReviewWebhook)
	webhooks.POST("/authorization.k8s.io/v1/subjectaccessreviews", c.Handler.SubjectAccessReviewWebhook)

	// The agent card is unauthenticated on purpose: a client that holds no
	// credential yet must be able to read it, and everything it points at is
	// signed. It is served at the origin-root well-known location the A2A
	// protocol defines, not under the transport mount: discovery is a protocol
	// constant, while Path/PathREST are the deployment's mount. It cannot be
	// registered by the handler set — middleware is inherited from the group,
	// so a route added under /v1 is signed however it is written — which is why
	// the public routes are mounted here.
	engine.GET(a2a.PathCard, c.Handler.A2ACard)

	// Unauthenticated for the same reason a probe is: the caller is a browser
	// rendering a message, and it holds no key. The objects are addressed by an
	// unguessable key instead — see Handler.ServeMedia. The route is registered
	// only when this deployment keeps its objects on disk; an object store
	// serves its own bytes, and the handler answers 404 when there is no local
	// store, so the route existing is never a claim that it can serve anything.
	engine.GET(media.PublicPath+"*objectKey", c.Handler.ServeMedia)

	// Read-only share links. A recipient holds a bearer share token, not the
	// service's AK/SK, so these routes sit outside the signed group and
	// authorize against the share row instead.
	share := engine.Group("/v1/share")
	share.GET("/sessions/:id/runs", c.Handler.SharedSessionRuns)
	share.GET("/runs/:id", c.Handler.SharedAgentRun)
	share.GET("/runs/:id/events", c.Handler.SharedRunEvents)

	// The run surface under /v1 is signed. See pkg/authn: the body is part of
	// the signed string, so the identity assertions in it cannot be rewritten in
	// flight, and the nonce store rejects a replay of a captured request within
	// the timestamp window.
	// The control-token gate rides after the signature check: an unsigned caller
	// should hear "unauthenticated" rather than learn that a second credential
	// exists. It only guards /v1/agent/authorizationrules and /v1/core/secrets;
	// every other signed path passes through.
	v1 := engine.Group("/v1",
		// AgentCall wraps the platform authenticator: an Agent that names
		// authentication Webhooks decides a Bearer token itself, while an
		// Agent that names none (and every non-invocation route) runs the
		// platform chain unchanged.
		c.AgentCall.Wrap(authn.AKSKAuth(c.Handler.SecretKey, c.BypassAKSK, c.Nonce)),
		authn.ControlToken(c.ControlToken),
		// After the signature check: an idempotency claim is a write, and an
		// unauthenticated caller must not be able to consume keys.
		mw.Idempotent(c.Idempotency),
	)
	c.Handler.ApplyTo(v1)

	// The declarative resource surface: one URL per resource, reachable by a
	// signed service call or by any signed-in browser user holding a bearer
	// token.
	//
	// This is a sibling group because gin fixes a route's middleware chain when
	// it is registered: the signed group above cannot accept a bearer token,
	// and the /v1/admin group below cannot accept AK/SK. UnifiedAuth selects on
	// credential presence (an AK/SK header never downgrades to JWT), so the
	// resource routes are mounted exactly once and the two callers share them.
	//
	// UserGuard is the browser-side policy that UnifiedAuth deliberately does
	// not carry: governance resources stay administrator-only, and writes to an
	// owned resource are restricted to its owner. It reads the claims
	// UnifiedAuth stored, so the order below is load-bearing.
	declarative := engine.Group("/v1",
		c.AgentCall.Wrap(authn.UnifiedAuth(authn.UnifiedAuthConfig{
			Provider:           c.Handler.SecretKey,
			BypassAKSK:         c.BypassAKSK,
			Nonces:             c.Nonce,
			JWTSecret:          c.JWTSecret,
			AnonymousAuth:      c.AnonymousAuth,
			AuthzMode:          c.AuthorizationMode,
			ControlToken:       c.ControlToken,
			TokenAuthenticator: c.TokenAuthenticator,
		})),
		c.Handler.UserGuard(),
		// After authentication: an idempotency claim is a write, and an
		// unauthenticated caller must not be able to consume keys.
		mw.Idempotent(c.Idempotency),
	)
	c.Handler.ApplyUnifiedTo(declarative)

	// The personal self-service surface: routes that act on the signed-in
	// account's own data and need no administrator role.
	//
	// It is a sibling group rather than a path on the declarative group because
	// UnifiedAuth's bearer branch requires the administrator role, and a user's
	// own access keys must be reachable by that user. The group authenticates
	// the token and adds no role check; each handler narrows to claims.UserID.
	selfService := engine.Group("/v1",
		bearer.AuthWithMode(c.JWTSecret, c.AnonymousAuth, c.AuthorizationMode),
		mw.Idempotent(c.Idempotency),
	)
	c.Handler.ApplySelfServiceTo(selfService)

	// The management surface: the same resources for a caller that is a browser.
	//
	// A browser holds neither the AK/SK pair nor a way to sign with it — the
	// secret cannot leave the server — so this face authenticates with a bearer
	// token instead, and checks the role before anything else is reached.
	//
	// # Why a sibling group and not a path inside /v1
	//
	// The two faces carry different authentication, and gin fixes a route's
	// handler chain when it is registered: a route created on this group is not
	// reached by the signed group's middleware, and vice versa. That is what
	// keeps the signature requirement off a request that cannot satisfy it, and
	// what makes the admin gate apply to every route here rather than to the
	// ones somebody remembered.
	//
	// The order below is load-bearing: RequireAdmin reads the claims
	// AuthWithMode stored, so it must come second.
	admin := engine.Group("/v1/admin",
		bearer.AuthWithMode(c.JWTSecret, c.AnonymousAuth, c.AuthorizationMode),
		bearer.RequireAdmin(),
	)
	c.Handler.ApplyAdminTo(admin)
}

// observabilityRouteSkips are the routes the request-observability middleware
// does not wrap when features.observability-skip is on.
//
// Each one is either long-lived or carries a body that should not be logged:
// a stream holds a request open for minutes (the body-capturing writer sits
// between the handler and the connection), and the chat body is the user's
// prompt. Skipping the route bypasses body capture, request logging and trace
// injection for it, which is the same treatment os-apiserver gives its own
// streaming endpoints.
//
// The probe paths are already skipped by the router; only this service's own
// streaming routes are listed here.
var observabilityRouteSkips = []string{
	"/v1/chat/completions",
	"/v1/agent/ask",
	// The A2A surface is both streaming and multi-transport: the JSON-RPC mount
	// is exact, and the trailing slash covers the REST subtree whose routes are
	// absolute (/message:send, /tasks/:id:subscribe, ...). Without it a REST
	// stream would be wrapped by the body-capturing writer.
	"/v1/chat/a2a",
	"/v1/chat/a2a/",
	// Share links carry their bearer token in the query string; logging the URL
	// would write the credential to the access log.
	"/v1/share",
	"/v1/share/",
	// The agent card at the origin root is deliberately not skipped: it is a
	// small public GET with no request body and no credential.
}
