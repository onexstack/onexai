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
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		// Authenticated, and then restricted to administrators.
		//
		// The on-call groups next door carry no role check — any caller with a
		// token can rewrite the duty rota — and that is a gap in existing code,
		// not a model to copy. A ticket is a record of who reported what and who
		// is handling it, and this read surface is new, so it gets the gate the
		// older one is missing.
		group := engine.Group("",
			h.AuthMiddleware(),
			bearer.RequireAdmin(),
		)
		tickets := group.Group("/tickets")
		tickets.GET("", h.ListTicket)
		tickets.GET("/:ticketID", h.GetTicket)
		// The handling log is a sub-resource rather than a column on the ticket:
		// it is a list of what /mark recorded, each entry with its own author
		// and timestamp.
		tickets.GET("/:ticketID/notes", h.ListTicketNote)
	})
}

// ListTicket handles GET /v1/tickets.
//
// It answers with the raw model rows, as the on-call groups do: the model's JSON
// tags are the one description of a ticket, and the console reads the subset it
// displays.
//
// The page is newest-first: the generic store appends ORDER BY id desc, and a
// ticket's auto-increment id follows its creation order (ticket_id is a
// time-ordered Sonyflake string). That is the ordering contract the console
// relies on, so a change here would reorder every ticket page.
func (h *Handler) ListTicket(c *gin.Context) {
	core.HandleRequest(c, c.ShouldBindQuery, h.biz.TicketV1().List, h.val.ValidateListTicketRequest)
}

// GetTicket handles GET /v1/tickets/:ticketID.
func (h *Handler) GetTicket(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.TicketV1().Get, h.val.ValidateGetTicketRequest)
}

// ListTicketNote handles GET /v1/tickets/:ticketID/notes.
//
// The notes come back oldest-first, the way they are read, and are capped
// rather than paged — see ticketbiz.MaxTicketNotes. total is the number the
// ticket actually has, so a log that reached the cap is visible as one.
func (h *Handler) ListTicketNote(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.TicketV1().ListNote, h.val.ValidateListTicketNoteRequest)
}
