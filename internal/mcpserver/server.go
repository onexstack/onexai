package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/onexstack/onexstack/pkg/server"
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/mcpserver/handler"
	"github.com/onexstack/onexai/internal/mcpserver/pkg/asyncstore"
	"github.com/onexstack/onexai/internal/mcpserver/pkg/clientset/typed/fake"
	"github.com/onexstack/onexai/internal/mcpserver/pkg/clientset/typed/oss"
	"github.com/onexstack/onexai/internal/mcpserver/pkg/metrics"
	mcpregistry "github.com/onexstack/onexai/internal/mcpserver/pkg/registry"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

const serviceName = "oai-mcpserver"

// Dependencies collects all components that need initialization but are not directly used
// by the main server struct during runtime (e.g., sidecar processes, cache warmers).
type Dependencies struct{}

// Config contains application-related configurations.
type Config struct {
	// Transport selects the MCP wire transport: stdio, sse, or streamable-http.
	Transport string
	// Addr is the listen address used by the sse and streamable-http
	// transports. It is ignored when Transport is stdio.
	Addr          string
	CoreDBOptions *customoptions.CoreDBOptions
	FakeOptions   *genericoptions.RestyOptions
	OssOptions    *genericoptions.RestyOptions
}

// Server represents the web server and its background workers.
type Server struct {
	cfg *ServerConfig
	srv server.Server
}

// ServerConfig contains the core dependencies and configurations of the server.
type ServerConfig struct {
	*Config
	DB           *gorm.DB
	Dependencies *Dependencies
}

// New creates and returns a new Server instance.
func (cfg *Config) New(ctx context.Context) (*Server, error) {
	// Create the core server instance using dependency injection.
	// This relies on the wire-generated NewServer function.
	s, err := NewServer(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return s.Prepare(ctx)
}

// Prepare performs post-initialization tasks such as registering subscribers.
func (s *Server) Prepare(ctx context.Context) (*Server, error) {
	_ = metrics.Init(serviceName)
	return s, nil
}

// Run starts the server and gracefully shuts it down on ctx cancellation.
func (s *Server) Run(ctx context.Context) error {
	return server.Serve(ctx, s.srv)
}

// NewDB creates and returns a *gorm.DB instance for database operations.
func (cfg *Config) NewDB() (*gorm.DB, error) {
	slog.Info("initializing database connection", "dialect", cfg.CoreDBOptions.Dialect)
	dbInstance, err := cfg.CoreDBOptions.NewDB()
	if err != nil {
		slog.Error("failed to create database connection", "error", err)
		return nil, err
	}

	// Automatically migrate database schema
	if err := registry.Migrate(dbInstance); err != nil {
		slog.Error("failed to migrate database schema", "error", err)
		return nil, err
	}

	return dbInstance, nil
}

// ProvideDB provides a database instance based on the configuration.
func ProvideDB(cfg *Config) (*gorm.DB, error) {
	return cfg.NewDB()
}

// ProvideFakeClient creates and returns a fake client instance using the provided configuration.
func ProvideFakeClient(cfg *Config) fake.Interface {
	return fake.NewForConfig(cfg.FakeOptions)
}

// ProvideOssClient creates and returns a oss client instance using the provided configuration.
func ProvideOssClient(cfg *Config) oss.Interface {
	return oss.NewForConfig(cfg.OssOptions)
}

// ProvideAStore creates and returns an asynchronous store factory.
func ProvideAStore(ctx context.Context) asyncstore.Factory {
	return asyncstore.NewStore(ctx, 30*time.Minute)
}

// NewDependencies initializes all components that need to be started but are not directly stored.
// This is typically used for side-effects or warming up caches.
func NewDependencies(ctx context.Context, _ asyncstore.Factory) *Dependencies {
	// Simulate cache warmup or check.
	fakeItem, _ := asyncstore.S.Fake().Get("fixed-item-001")
	slog.DebugContext(ctx, "successfully retrieved fake cache data", "data", fakeItem.String())

	return &Dependencies{}
}

// Transport identifies which MCP wire transport the server listens on.
const (
	// TransportStdio speaks newline-delimited JSON-RPC over stdin/stdout.
	TransportStdio = "stdio"
	// TransportSSE speaks the 2024-11-05 HTTP+SSE MCP transport.
	TransportSSE = "sse"
	// TransportStreamableHTTP speaks JSON-RPC over a single HTTP POST
	// endpoint, per the 2025-03-26 MCP streamable-HTTP transport.
	TransportStreamableHTTP = "streamable-http"
)

// NewTransportServer constructs the server.Server implementation for
// cfg.Transport. Each case wraps the same *handler.Handler; only the wire
// framing differs between transports. cfg.Transport is guaranteed to be one
// of the three valid values by ServerOptions.Validate before it ever reaches
// here, so no fallback-to-stdio case is needed.
func NewTransportServer(serverConfig *ServerConfig) (server.Server, error) {
	reg := mcpregistry.Default()
	for _, tool := range reg.List("") {
		slog.Info("registered tool", "namespace", tool.Namespace, "name", tool.Name)
	}

	hdl := handler.NewHandler(reg)

	switch serverConfig.Transport {
	case TransportStdio:
		return NewStdioServer(hdl, os.Stdin, os.Stdout), nil
	case TransportSSE:
		return NewSSEServer(hdl, serverConfig.Addr)
	case TransportStreamableHTTP:
		return NewStreamableHTTPServer(hdl, serverConfig.Addr)
	default:
		return nil, fmt.Errorf("unknown mcp transport %q", serverConfig.Transport)
	}
}
