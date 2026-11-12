package cobrax

import (
	"context"
	"fmt"
	"log/slog"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// ServeOptions configures how a Transport binds and advertises itself.
type ServeOptions struct {
	// Addr is the listen address for network transports (e.g. ":8090").
	// It is ignored by StdioTransport.
	Addr string

	// BaseURL is the publicly reachable base URL for network transports. When
	// empty, it is derived from Addr (normalising ":port" to "localhost:port").
	BaseURL string
}

// Transport serves an MCP server over a specific wire protocol.
//
// Implementations must block until the context is cancelled or an interrupt
// signal (SIGINT/SIGTERM) is received, then shut down gracefully. Returning an
// error indicates the transport failed to start or shut down.
type Transport interface {
	Serve(ctx context.Context, srv *mcpserver.MCPServer, opts ServeOptions) error
}

// StdioTransport serves the MCP server over standard input/output. It is the
// transport used by editor integrations (Claude Desktop, VSCode, Cursor).
type StdioTransport struct{}

// Serve implements Transport.
func (StdioTransport) Serve(_ context.Context, srv *mcpserver.MCPServer, _ ServeOptions) error {
	return mcpserver.ServeStdio(srv)
}

// SSETransport serves the MCP server over Server-Sent Events (HTTP).
type SSETransport struct{}

// Serve implements Transport.
func (SSETransport) Serve(ctx context.Context, srv *mcpserver.MCPServer, opts ServeOptions) error {
	if opts.Addr == "" {
		return fmt.Errorf("cobrax: SSETransport requires a non-empty ServeOptions.Addr")
	}

	baseURL := sseBaseURL(opts.Addr, opts.BaseURL)

	var sseOpts []mcpserver.SSEOption
	if baseURL != "" {
		sseOpts = append(sseOpts, mcpserver.WithBaseURL(baseURL))
	}
	sseServer := mcpserver.NewSSEServer(srv, sseOpts...)

	go func() {
		<-shutdownSignal(ctx)
		if err := sseServer.Shutdown(context.Background()); err != nil {
			slog.Error("error shutting down MCP SSE server", "error", err)
		}
	}()

	slog.Info("MCP SSE server listening", "addr", opts.Addr, "baseURL", baseURL)
	if err := sseServer.Start(opts.Addr); err != nil {
		return fmt.Errorf("MCP SSE server error: %w", err)
	}
	return nil
}

// sseBaseURL derives the public base URL for an SSE server from the listen
// address, normalising ":port" to "localhost:port".
func sseBaseURL(addr, baseURL string) string {
	if baseURL != "" {
		return baseURL
	}
	baseURL = fmt.Sprintf("http://%s", addr)
	if len(addr) > 0 && addr[0] == ':' {
		baseURL = fmt.Sprintf("http://localhost%s", addr)
	}
	return baseURL
}
