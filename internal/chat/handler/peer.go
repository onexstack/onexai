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

package handler

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/restbind"
)

// headerInternalToken carries the bearer value a service-to-service caller
// presents. It is a header of its own rather than Authorization: the browser
// face of this service reads that one, and a shared secret arriving where a
// user token is expected would be a category error waiting to be made.
const headerInternalToken = "X-Internal-Token"

// ctxInternalSubject is where the authenticated caller's subject is parked for
// the handlers below. It is the value the credential was configured with, never
// anything from the request.
const ctxInternalSubject = "internal.subject"

// internalAuth authenticates a service-to-service caller against the configured
// credentials.
//
// # Why the caller's subject cannot come from the request
//
// Every handler behind this reads the subject from the context. A subject the
// caller could supply is one it could choose, and the whole point of naming a
// subject is that it says who wrote a row — a value the writer picks says
// nothing.
//
// # Why the comparison does not stop at the first match
//
// The loop walks every credential and records the match rather than returning
// from inside it. Returning early would make the response time depend on how
// far down the list the caller's secret sits, which is enough to learn the
// list's order and to confirm a guess by its position.
//
// # With nothing configured it refuses
//
// An empty credential list is a deployment with no such integrator, and the
// safe reading of that is "these endpoints are closed". Treating it as "no
// authentication required" would open them on exactly the deployments that
// never intended to expose them.
func (h *Handler) internalAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(h.InternalCredentials) == 0 {
			core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage(
				"the peer endpoints are not configured on this deployment"))
			c.Abort()
			return
		}

		presented := c.GetHeader(headerInternalToken)
		subject := ""
		for _, cred := range h.InternalCredentials {
			// ConstantTimeCompare on an empty presented value still runs; the
			// length check before it keeps a short secret from being padded into
			// a comparison that always fails slowly.
			matched := len(presented) > 0 &&
				subtle.ConstantTimeCompare([]byte(presented), []byte(cred.Secret)) == 1
			if matched {
				subject = cred.Subject
			}
		}
		if subject == "" {
			c.Header("WWW-Authenticate", "X-Internal-Token")
			core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage(
				"a valid internal token is required"))
			c.Abort()
			return
		}

		c.Set(ctxInternalSubject, subject)
		c.Next()
	}
}

// callerSubject returns the authenticated subject set by internalAuth.
func callerSubject(c *gin.Context) string {
	s, _ := c.Get(ctxInternalSubject)
	out, _ := s.(string)
	return out
}

// GetPeerBindClaim serves GET /v1/peer-bind-claims/:code.
//
// It is the read half of the binding handshake: the chat side parked which chat
// identity spoke the code, and this is how the business side comes to collect
// it. A missing or expired code answers 404 — the caller has nothing to act on
// either way, and telling "never existed" from "expired" would only say which
// guesses were once real.
func (h *Handler) GetPeerBindClaim(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ChatV1().PeerBindClaim, h.val.ValidateGetPeerBindClaimRequest)
}

// PutPeerAgent serves PUT /v1/peer-agents/:backend/:scope/:peerID.
//
// # It upserts, so a retry is not a second binding
//
// The conflict target is the triple, and the write is idempotent. A caller
// pushing the same binding twice — a retry after a timeout it did not see the
// answer to — leaves one row, not two.
func (h *Handler) PutPeerAgent(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *api.PutPeerAgentRequest) {
		rq.Backend = c.Param("backend")
		rq.Scope = c.Param("scope")
		rq.PeerID = c.Param("peerID")
		rq.Source = callerSubject(c)
	}), h.biz.ChatV1().PutPeerAgent, h.val.ValidatePutPeerAgentRequest)
}

// GetPeerAgent serves GET /v1/peer-agents/:backend/:scope/:peerID.
//
// It exists for the console to show what a user's binding currently says, and
// for whoever is diagnosing one. The agent service's own view is not readable
// from here, so this is the only way to see the projection directly.
func (h *Handler) GetPeerAgent(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ChatV1().PeerAgent, h.val.ValidatePeerAgentRequest)
}

// DeletePeerAgent serves DELETE /v1/peer-agents/:backend/:scope/:peerID.
//
// Deleting one that is not there is a success: unbinding twice leaves the same
// world as unbinding once, and answering 404 would make a caller that retried
// treat its own retry as an error.
func (h *Handler) DeletePeerAgent(c *gin.Context) {
	var rq api.PeerAgentRequest
	if err := core.ReadRequest(c, &rq, c.ShouldBindUri, h.val.ValidatePeerAgentRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if err := h.biz.ChatV1().DeletePeerAgent(c.Request.Context(), &rq); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		g := engine.Group("", h.internalAuth())
		g.GET("/peer-bind-claims/:code", h.GetPeerBindClaim)
		g.PUT("/peer-agents/:backend/:scope/:peerID", h.PutPeerAgent)
		g.GET("/peer-agents/:backend/:scope/:peerID", h.GetPeerAgent)
		g.DELETE("/peer-agents/:backend/:scope/:peerID", h.DeletePeerAgent)
	})
}
