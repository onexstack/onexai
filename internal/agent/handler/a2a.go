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

package handler

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	agent2a "github.com/onexstack/onexai/internal/agent/pkg/a2a"
	"github.com/onexstack/onexai/internal/agent/pkg/interactionlog"
	mw "github.com/onexstack/onexai/internal/pkg/middleware/gin"
)

// A2AHandlers are the Agent-to-Agent transports, built by the package that owns
// the SDK. All three are always set: the routes exist in every deployment so a
// client can tell "not offered" from "wrong URL".
type A2AHandlers struct {
	// JSONRPC serves POST /v1/chat/a2a.
	JSONRPC http.Handler
	// REST serves everything under /v1/chat/a2a/rest.
	REST http.Handler
	// Card serves the origin-root agent card a client fetches to discover the
	// endpoint.
	Card http.Handler
}

// NewA2AGuard adapts the edge's inbound audit to the A2A adapter's port.
//
// The adapter owns the turn and the edge owns the policy, so screening stays
// where every other surface's screening already is: one screener, one metrics
// counter, one interaction row per refusal. A nil screener returns a nil guard,
// which the adapter reads as "this deployment does not screen".
func NewA2AGuard(screen Screener, interactions *interactionlog.Writer) agent2a.Guard {
	if screen == nil {
		return nil
	}
	return &a2aGuard{screen: screen, interactions: interactions}
}

// a2aGuard is the inbound audit behind the A2A surface.
type a2aGuard struct {
	screen       Screener
	interactions *interactionlog.Writer
}

var _ agent2a.Guard = (*a2aGuard)(nil)

// Screen implements agent2a.Guard.
func (g *a2aGuard) Screen(ctx context.Context, turn agent2a.Turn) (string, bool) {
	verdict, blocked := screenInput(ctx, g.screen, g.interactions, BlockedInput{
		Source:     turn.Source,
		SessionKey: turn.SessionKey,
		SenderID:   turn.SenderID,
		SenderName: turn.SenderName,
	}, turn.Text)
	if !blocked {
		return "", false
	}
	return verdict.UserMessage(), true
}

// A2AJSONRPC handles POST /v1/chat/a2a.
func (h *Handler) A2AJSONRPC(c *gin.Context) {
	h.prepareA2A(c)
	h.a2a.JSONRPC.ServeHTTP(c.Writer, c.Request)
}

// A2AREST handles the REST transport under /v1/chat/a2a/rest.
func (h *Handler) A2AREST(c *gin.Context) {
	h.prepareA2A(c)
	http.StripPrefix(agent2a.PathREST, h.a2a.REST).ServeHTTP(c.Writer, c.Request)
}

// A2ACard serves the agent card at the origin-root well-known path
// (agent2a.PathCard).
//
// It is deliberately public: a client that holds no credential yet must be able
// to read it, and everything it points at is signed. It is also deliberately
// not prepared like the two transports — a card is one small response, so there
// is no deadline to lift and nothing runs for an output ceiling to apply to.
func (h *Handler) A2ACard(c *gin.Context) {
	h.a2a.Card.ServeHTTP(c.Writer, c.Request)
}

// prepareA2A makes the request runnable by the SDK's transports.
//
// Two things the edge owes every run happen here because these routes hand the
// request to a raw http.Handler and never reach AskAgent: the connection's
// absolute deadlines are lifted (a streamed answer outlives them, exactly as it
// does on the SSE surfaces — see openSSE), and the per-caller output ceiling is
// attached to the context the adapter runs with.
func (h *Handler) prepareA2A(c *gin.Context) {
	if err := detachConnDeadlines(mw.ConnWriter(c)); err != nil {
		// Advisory: the response is written either way, and the run's own
		// timeout bounds what a later write timeout would cut short.
		slog.WarnContext(c.Request.Context(), "a2a: could not lift the connection deadline", "error", err)
	}
	c.Request = c.Request.WithContext(h.askContext(c))
}

func init() { //nolint:gochecknoinits
	Register(func(v1Group *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		// mws carries the group's middleware (AK/SK authentication). The A2A
		// transport handlers are raw http.Handlers, so gin has to be told to
		// run them.
		//
		// The paths are the surface's own constants with the group prefix
		// removed: writing them out again here is how a moved route ends up
		// advertised by a card that still points at the old one.
		base := strings.TrimPrefix(agent2a.Path, "/v1")
		rest := strings.TrimPrefix(agent2a.PathREST, "/v1")

		v1Group.POST(base, append(slices.Clone(mws), h.A2AJSONRPC)...)
		// The REST transport's routes are absolute (/message:send, /tasks, ...),
		// so it gets a catch-all subtree of its own under PathREST; the JSON-RPC
		// mount is the exact Path. Keeping the two transports in separate
		// subtrees is what lets gin build both trees without a catch-all/static
		// conflict.
		//
		// The card is not registered here on purpose. Middleware is inherited
		// from the group, so a route added to the signed /v1 group is signed
		// however it is written; the card has to be public, and it lives at the
		// origin root (PathCard). The server package mounts it on the bare
		// router itself. See agent.ServerConfig.InstallRESTAPI.
		v1Group.Any(rest+"/*any", append(slices.Clone(mws), h.A2AREST)...)
	})
}
