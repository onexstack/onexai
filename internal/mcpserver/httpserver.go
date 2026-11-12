// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/server"

	"github.com/onexstack/onexai/internal/mcpserver/pkg/protocol"
)

// headerSessionID and headerProtocolVersion are the two HTTP headers the
// streamable-HTTP transport spec (2025-03-26+) layers on top of plain
// JSON-RPC: a session identifier issued at initialize and echoed back by
// the client on every later request, and the protocol version negotiated
// during initialize.
const (
	headerSessionID       = "Mcp-Session-Id"
	headerProtocolVersion = "MCP-Protocol-Version"
)

// httpServer implements the server.Server interface over a plain
// *http.Server, playing the same role for the sse and streamable-http
// transports that stdioServer plays for stdio. Both transports differ only
// in the mux they install; the listen/shutdown lifecycle is shared here.
type httpServer struct {
	transport string
	srv       *http.Server
	lis       net.Listener
}

// Ensure *httpServer implements the server.Server interface.
var _ server.Server = (*httpServer)(nil)

// newHTTPServer binds addr and wraps handler in an httpServer, mirroring
// onexstack/pkg/server.NewGRPCServer's listen-then-wrap pattern.
func newHTTPServer(transport string, addr string, handler http.Handler) (*httpServer, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %w", err)
	}

	return &httpServer{
		transport: transport,
		srv:       &http.Server{Handler: handler},
		lis:       lis,
	}, nil
}

// Run starts serving HTTP requests until the listener is closed via
// GracefulStop. It never exits the process on error: a stopped MCP server
// is not a fatal condition for the surrounding CLI process.
func (s *httpServer) Run(ctx context.Context) error {
	slog.InfoContext(ctx, "start to listening the incoming requests", "transport", s.transport, "protocol", "http", "addr", s.lis.Addr().String())

	if err := s.srv.Serve(s.lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.ErrorContext(ctx, "failed to serve http server", "error", err)
		return err
	}
	return nil
}

// GracefulStop gracefully shuts down the HTTP server, waiting for in-flight
// requests (including open SSE streams) to finish or ctx to expire.
func (s *httpServer) GracefulStop(ctx context.Context) error {
	slog.InfoContext(ctx, "gracefully stop http server")

	if err := s.srv.Shutdown(ctx); err != nil {
		slog.ErrorContext(ctx, "http server forced to shutdown", "error", err)
		return err
	}
	return nil
}

// ---- StreamableHTTP transport (2025-03-26 MCP transport spec, simplified) ----
//
// The handler is stateless per-request, so this transport is just JSON-RPC
// carried over a single HTTP POST: the client posts a request body and gets
// the JSON-RPC response back as the HTTP response body. Notifications (no
// request ID) get an empty 202 Accepted, since JSON-RPC defines no response
// for them.
//
// Two spec-mandated headers are layered on top without introducing real
// server-side session state (the handler underneath has none to track):
// initialize responses carry a freshly minted Mcp-Session-Id, and later
// requests that name a protocol version via MCP-Protocol-Version are
// rejected if it isn't the one version this server speaks.

// NewStreamableHTTPServer creates a new streamable-http-transport MCP server
// that dispatches requests to h, listening on addr.
func NewStreamableHTTPServer(h RequestHandler, addr string) (*httpServer, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", streamableHTTPHandler(h))

	return newHTTPServer(TransportStreamableHTTP, addr, mux)
}

func streamableHTTPHandler(h RequestHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req protocol.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusOK, protocol.NewErrorResponse(nil, protocol.CodeParseError, err.Error(), nil))
			return
		}

		if req.Method != protocol.MethodInitialize {
			if v := r.Header.Get(headerProtocolVersion); v != "" && v != protocol.ProtocolVersion {
				writeJSON(w, http.StatusBadRequest, protocol.NewErrorResponse(
					req.ID, protocol.CodeInvalidRequest,
					fmt.Sprintf("unsupported %s %q: server speaks %q", headerProtocolVersion, v, protocol.ProtocolVersion), nil))
				return
			}
		}

		resp := h.Handle(r.Context(), &req)

		if req.Method == protocol.MethodInitialize {
			w.Header().Set(headerSessionID, uuid.NewString())
		}

		if resp == nil {
			// Notifications get no JSON-RPC response.
			w.WriteHeader(http.StatusAccepted)
			return
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// ---- SSE transport (2024-11-05 HTTP+SSE MCP transport) ----
//
// GET /sse opens a long-lived event stream and announces a per-connection
// POST endpoint. POST /messages?sessionId=<id> carries JSON-RPC requests in;
// responses are pushed back as "message" events on the matching /sse stream,
// not in the POST response body.

// NewSSEServer creates a new sse-transport MCP server that dispatches
// requests to h, listening on addr.
func NewSSEServer(h RequestHandler, addr string) (*httpServer, error) {
	sessions := newSSESessions()

	mux := http.NewServeMux()
	mux.HandleFunc("/sse", sseHandler(sessions))
	mux.HandleFunc("/messages", messagesHandler(h, sessions))

	return newHTTPServer(TransportSSE, addr, mux)
}

func sseHandler(sessions *sseSessions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		sessionID := uuid.NewString()
		ch := sessions.register(sessionID)
		defer sessions.unregister(sessionID)

		fmt.Fprintf(w, "event: endpoint\ndata: /messages?sessionId=%s\n\n", sessionID)
		flusher.Flush()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case resp := <-ch:
				b, err := protocol.Encode(resp)
				if err != nil {
					slog.ErrorContext(ctx, "failed to marshal sse response", "error", err)
					continue
				}

				fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
				flusher.Flush()
			}
		}
	}
}

func messagesHandler(h RequestHandler, sessions *sseSessions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		sessionID := r.URL.Query().Get("sessionId")
		ch, ok := sessions.get(sessionID)
		if !ok {
			http.Error(w, "unknown sessionId", http.StatusNotFound)
			return
		}

		var req protocol.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp := protocol.NewErrorResponse(nil, protocol.CodeParseError, err.Error(), nil)
			if !deliver(r.Context(), ch, resp) {
				slog.ErrorContext(r.Context(), "failed to deliver sse parse-error response: reader gone or timed out", "sessionId", sessionID)
				http.Error(w, "failed to deliver response to sse stream", http.StatusGatewayTimeout)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}

		resp := h.Handle(r.Context(), &req)
		if resp == nil {
			// Notifications get no JSON-RPC response to deliver.
			w.WriteHeader(http.StatusAccepted)
			return
		}

		if !deliver(r.Context(), ch, resp) {
			slog.ErrorContext(r.Context(), "failed to deliver sse response: reader gone or timed out", "sessionId", sessionID)
			http.Error(w, "failed to deliver response to sse stream", http.StatusGatewayTimeout)
			return
		}

		w.WriteHeader(http.StatusAccepted)
	}
}

func writeJSON(w http.ResponseWriter, status int, resp *protocol.Response) {
	b, err := protocol.Encode(resp)
	if err != nil {
		slog.Error("failed to marshal mcp http response", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
