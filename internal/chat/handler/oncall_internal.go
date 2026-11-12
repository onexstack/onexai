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
	"context"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/pkg/validation"
	"github.com/onexstack/onexai/internal/pkg/restbind"
)

// The on-call read surface: the two reads a downstream consumer polls to turn
// finished tickets into something learners can read.
//
// # Why it is on the internal face and not beside /v1/tickets
//
// /v1/tickets answers an operator holding an admin token, and it answers with
// whatever the caller asks for. This answers a *service*, on a schedule, with
// no person in the loop — so it is guarded by the shared internal credential
// rather than by a role, and it is deliberately narrow: two reads, no writes,
// and a caller that has to name the duty groups it wants rather than being
// handed everything.
//
// The two faces are also separated because they are different audiences with
// different blast radii. Widening this one cannot widen the operator one, and
// the credential a consumer holds is not a credential that can rewrite a duty
// rota.
func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		g := engine.Group("/internal/oncall", h.internalAuth())
		g.GET("/tickets", h.ListOncallTicket)
		g.GET("/tickets/:ticketID", h.GetOncallTicket)
	})
}

// ListOncallTicket serves GET /v1/internal/oncall/tickets.
//
// Each candidate carries the ticket row and its handling log, because the log
// is what a consumer hashes to decide whether anything changed. It answers the
// requested groups back untouched: a poll that found nothing should be able to
// say which roster it searched, and "we asked for these five groups and got
// zero" is the shape a mistyped group id shows up in.
func (h *Handler) ListOncallTicket(c *gin.Context) {
	core.HandleQueryRequest(c, func(
		ctx context.Context, rq *validation.ListOncallTicketRequest,
	) (*api.ListOncallTicketResponse, error) {
		return h.biz.OncallV1().ListForSyncView(ctx, rq, h.appID())
	}, h.val.ValidateListOncallTicketRequest)
}

// GetOncallTicket serves GET /v1/internal/oncall/tickets/:ticketID.
//
// It answers for a ticket in ANY status, and says in `eligible` whether the
// caller's statuses still admit it. That is the whole reason it exists: a
// consumer re-reading a ticket it already acted on needs to tell "this is still
// something to publish" apart from "this was reopened, and the thing I published
// no longer describes it" — and a read that answered not-found for the second
// would say the same thing it says about a ticket that never existed.
func (h *Handler) GetOncallTicket(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[validation.GetOncallTicketRequest](c),
		func(ctx context.Context, rq *api.GetOncallTicketRequest) (*api.GetOncallTicketResponse, error) {
			return h.biz.OncallV1().TicketView(ctx, rq, h.appID())
		}, h.val.ValidateGetOncallTicketRequest)
}

// appID is the Lark application these identities are scoped to, or empty when
// this deployment has not configured one.
//
// Empty is passed on rather than refused: a deployment with no Lark app has no
// tickets to sync either, and failing the read would turn "nothing configured"
// into an error the consumer would retry forever. A consumer that sees an empty
// application id resolves nobody, which is the truth about this deployment.
func (h *Handler) appID() string {
	if h.larkOptions == nil {
		return ""
	}
	return h.larkOptions.AppID
}
