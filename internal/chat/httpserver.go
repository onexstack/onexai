package chat

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/server"

	"github.com/onexstack/onexai/internal/pkg/httpserver"
)

// ginServer implements the server.Server interface using the Gin framework.
type ginServer struct {
	srv server.Server
}

// Ensure *ginServer implements the server.Server interface.
var _ server.Server = (*ginServer)(nil)

// NewGinServer initializes and returns a new HTTP server based on Gin.
// It returns the server.Server interface to abstract implementation details.
func (c *ServerConfig) NewGinServer() (server.Server, error) {
	engine := httpserver.NewRouter(httpserver.Options{
		ServiceName:        serviceName,
		FeatureOptions:     c.FeatureOptions,
		ObservabilitySkips: observabilityRouteSkips,
	})

	c.InstallRESTAPI(engine)

	httpsrv := server.NewHTTPServer(c.InsecureServingOptions, c.SecureServingOptions, engine)

	return &ginServer{srv: httpsrv}, nil
}

// InstallRESTAPI registers all RESTful API routes to the engine.
func (c *ServerConfig) InstallRESTAPI(engine *gin.Engine) {
	engine.GET("/healthz", c.Handler.Healthz)

	// Readiness is a separate answer from liveness: a database that is away must
	// take this pod out of the load balancer, not restart it. See
	// internal/pkg/readiness.
	engine.GET("/readyz", gin.WrapH(c.Readiness.Handler()))

	// OIDC discovery and JWKS. They are public by protocol: an authenticator
	// must be able to fetch them before it holds any credential. They are only
	// mounted when the deployment enabled OIDC, so the endpoint existing is
	// never a claim that token issuance is on.
	if c.OIDCSigner != nil {
		engine.GET("/.well-known/openid-configuration", c.OIDCSigner.DiscoveryHandler())
		engine.GET("/keys", c.OIDCSigner.JWKSHandler())
	}

	v1 := engine.Group("/v1")
	c.Handler.ApplyTo(v1)
}

// Run starts the Gin server and returns when it stops, either gracefully or
// with an error.
func (s *ginServer) Run(ctx context.Context) error {
	return s.srv.Run(ctx)
}

// GracefulStop gracefully shuts down the server.
func (s *ginServer) GracefulStop(ctx context.Context) error {
	return s.srv.GracefulStop(ctx)
}

/*
// InstallRESTAPI registers all RESTful API routes to the engine.
func InstallRESTAPI(engine *gin.Engine, cfg *Config, h *handler.Handler) {
	// REST API routes under /v1.
	v1 := engine.Group("/v1")

	// Public endpoints — no auth middleware.
	v1.POST("/auth/login", h.Login)

	// Authenticated endpoints — use AuthWithMode to optionally bypass JWT.
	authed := v1.Group("", bearer.AuthWithMode(h.JWTSecret, h.AnonymousAuth, h.AuthzMode))
	h.ApplyTo(authed)
}
*/

// observabilityRouteSkips are the routes the request-observability middleware
// does not wrap when features.observability-skip is on.
//
// The WebSocket and the Lark webhook are long-lived or high-volume, and the
// webhook body is an encrypted platform payload that has no business in a log
// line. Skipping a route bypasses body capture, request logging and trace
// injection for it, which is the same treatment os-apiserver gives its own
// webhook and WebSocket endpoints.
//
// The probe paths are already skipped by the router; only this service's own
// long-lived routes are listed here.
var observabilityRouteSkips = []string{
	"/lark/event",
	"/v1/ws",
}
