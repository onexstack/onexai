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

package mcpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/feizaonet/cobrax"
	mcpsdk "github.com/mark3labs/mcp-go/server"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	"github.com/onexstack/onexai/pkg/known"
	"github.com/onexstack/onexai/pkg/options"
)

// shutdownTimeout bounds the graceful drain of the MCP listener on shutdown.
const shutdownTimeout = 10 * time.Second

// transport serves the MCP server over streamable HTTP on its own listener.
//
// cobrax ships an SSE transport, and the built-in Start uses it. This is a
// custom one for three reasons that the SSE transport has no place for:
//
//   - Streamable HTTP is what oai-agent-apiserver's client speaks to the servers it
//     already connects (see internal/agent/pkg/mcpclient).
//   - Stateless mode is required. mcp-go's default session manager rejects any
//     request that arrives without the session id it handed out during
//     initialize — a bare tools/list probe gets a 404 "Invalid session ID"
//     instead of a protocol-level answer. This server rebuilds the command tree
//     for every call anyway, so it has no session state to lose, and going
//     stateless also means replicas need no sticky routing.
//   - The request has to be authenticated and attributed before it reaches the
//     MCP protocol layer, which cobrax's Transport interface does not expose.
type transport struct {
	opts *options.MCPOptions
	// sr resolves the caller's roles. It is the deployment's own runtime rather
	// than anything read off the wire, because the wire carries an identity and
	// no claims — the SRE and admin lists live here, so one list means the same
	// thing on every surface that consults it.
	sr *runtime.StaticRuntime

	// current is the handler of the server currently published. Bridge swaps it
	// when the custom-command catalog changes; a nil slot means Serve should
	// fall back to the server it was handed (the static use in the tests).
	current atomic.Pointer[handlerSlot]
}

// handlerSlot lets an http.Handler live behind an atomic pointer.
type handlerSlot struct {
	h http.Handler
}

// newTransport creates the streamable-HTTP transport for opts.
func newTransport(opts *options.MCPOptions, sr *runtime.StaticRuntime) *transport {
	return &transport{opts: opts, sr: sr}
}

// setServer publishes srv as the server every subsequent request is served by.
func (t *transport) setServer(srv *mcpsdk.MCPServer) {
	t.current.Store(&handlerSlot{h: streamableHandler(srv)})
}

// streamableHandler wraps srv for stateless streamable HTTP.
//
// Stateless is required: mcp-go's default session manager rejects a request
// that arrives without the session id it handed out during initialize — a bare
// tools/list probe gets a 404 instead of a protocol-level answer. This bridge
// rebuilds the command tree for every call anyway, so it has no session state
// to lose, and stateless also means replicas need no sticky routing.
func streamableHandler(srv *mcpsdk.MCPServer) http.Handler {
	return mcpsdk.NewStreamableHTTPServer(srv, mcpsdk.WithStateLess(true))
}

var _ cobrax.Transport = (*transport)(nil)

// Serve implements cobrax.Transport.
//
// It blocks until ctx is cancelled. Unlike cobrax's own transports it does not
// install its own signal handler: this server runs inside the application,
// which already cancels ctx on SIGINT/SIGTERM and waits for its servers to
// return, so listening for signals here would only make two places responsible
// for the same shutdown.
func (t *transport) Serve(ctx context.Context, srv *mcpsdk.MCPServer, opts cobrax.ServeOptions) error {
	if opts.Addr == "" {
		return fmt.Errorf("mcpserver: listen address must not be empty")
	}

	// Install the static server as the fallback the first time Serve is called.
	// Bridge has usually installed its own handler before Start, and the CAS
	// keeps that value from being overwritten by the server Start was given.
	if srv != nil {
		t.current.CompareAndSwap(nil, &handlerSlot{h: streamableHandler(srv)})
	}

	// The MCP handler dispatches on method alone and never inspects the path
	// (mcp-go's own Start mounts it at a fixed path instead). Both routes are
	// mounted so a client configured with the bare host and one configured with
	// an explicit "/mcp" both work; ServeMux prefers the more specific pattern,
	// so /healthz still wins.
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := t.current.Load()
		if current == nil {
			http.Error(w, "MCP server is not ready", http.StatusServiceUnavailable)
			return
		}
		current.h.ServeHTTP(w, r)
	})
	handler := t.requireToken(t.attributeMCPCaller(dispatch))
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/mcp", handler)
	mux.Handle("/", handler)

	httpSrv := &http.Server{
		Addr:    opts.Addr,
		Handler: mux,
		// A header timeout and no write timeout: the protocol holds responses
		// open while it streams, so a write deadline would cut live calls off.
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("failed to shut down the MCP listener", "error", err)
		}
	}()

	slog.Info("oai-chat-apiserver MCP listener started",
		"addr", opts.Addr, "path", "/mcp", "name", t.opts.Name, "auth", t.opts.Token != "")

	// ErrServerClosed is the ordinary result of the shutdown above, not a
	// failure: reporting it would make a clean stop look like a crash.
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("MCP HTTP server: %w", err)
	}
	return nil
}

// requireToken rejects requests that do not carry the configured bearer token.
//
// With no token configured it is a pass-through. That is the documented
// default: this listener is expected to sit on a loopback or cluster-internal
// address, and the token is what a deployment adds when it does not.
func (t *transport) requireToken(next http.Handler) http.Handler {
	if t.opts.Token == "" {
		return next
	}
	// Compare in constant time: a byte-by-byte comparison leaks how much of the
	// token a caller guessed right, which is enough to recover it.
	want := []byte("Bearer " + t.opts.Token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="oai-chat-apiserver"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// attributeMCPCaller puts the calling identity and its roles into the request
// context, where the command tree reads them from.
//
// The identity decides which records a job/cronjob tool may see and own: those
// commands scope every query by the caller, so a call with no identity is
// refused rather than attributed to a shared owner. The header is the wire form
// of that identity; internal/agent/pkg/mcpclient sends it per request,
// reading it from the run's context.
//
// A missing header falls back to the configured mcp.user-id, which is empty by
// default. Empty is the safe direction: the tools refuse loudly, rather than
// writing rows nobody owns.
//
// The roles are what the command tree's x-sre-only and x-admin-only annotations
// are checked against, and they are resolved here from that identity because the
// wire carries no claims. Leaving them unset is not a neutral default but a
// refusal: ValidateCommands reads a false role as "not that role", so every
// gated tool — pipeline, job, cronjob, duty and the rest — answered "requires
// SRE role" to callers this very deployment's IsSREUser answers true for. It is
// the same resolution the Lark surfaces make (see biz/v1/bot/event.go), which is
// what keeps one SRE list meaning one thing.
//
// This relies on the MCP server deriving the tool call's context from
// r.Context() — it does, on both the POST and GET paths of the streamable
// transport, so a value stored here is visible to the command.
func (t *transport) attributeMCPCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subject := strings.TrimSpace(r.Header.Get(string(known.XUserID)))
		if subject == "" {
			subject = strings.TrimSpace(t.opts.UserID)
		}

		dr := &runtime.DynamicRuntime{
			Source: runtime.RequestSourceMCP,
			// An MCP caller is a Sender with an identity and no platform handle —
			// nothing binds an open_id to a tool call.
			Sender: v1.NewSender(subject, ""),
			// A nil runtime answers false for both roles rather than true, which
			// is the safe direction here as well: an unconfigured deployment
			// refuses the gated tools instead of granting them to everyone.
			IsSRE:   t.sr.IsSREUser(subject),
			IsAdmin: t.sr.IsAdminUser(subject),
		}
		// The agents the caller runs as narrow the command tree — and with it the
		// tool list and the grant gate — to what they were granted. This is the
		// same answer the bot hands over for a conversation (ResolvedAgents), only
		// stated on the wire rather than settled by a scheduler, so both surfaces
		// filter by one rule. An absent header names nothing, which leaves every
		// command in place: a caller that does not say which agents it runs as
		// must not lose commands over it.
		if agents := agentNames(r); len(agents) > 0 {
			dr.ResolvedAgents = runtime.FixedAgents(agents)
		}
		next.ServeHTTP(w, r.WithContext(runtime.Inject(r.Context(), dr)))
	})
}

// agentNames reads the agents an MCP call runs as, from the x-agent-names header.
//
// The header is a comma-separated list because a run may select several agents
// and the wire carries them in one field; the values are trimmed and the empties
// dropped, so a trailing comma or a padded list still names only real agents.
func agentNames(r *http.Request) []string {
	raw := r.Header.Get(string(known.XAgentNames))
	if raw == "" {
		return nil
	}
	var agents []string
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			agents = append(agents, name)
		}
	}
	return agents
}
