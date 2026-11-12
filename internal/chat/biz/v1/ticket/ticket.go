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

// Package ticket implements the read-only ticket surface.
//
// # Why read-only
//
// A ticket's lifecycle belongs to the duty commands and the Lark cards around
// them: /open creates one, /hand routes it, /level grades it, /close resolves
// it. Those paths also mirror the row to the incident platform, so a second
// writer would be a second place where "what happened to this ticket" is
// decided — and one of the two would be a mirror that disagrees without either
// being able to see it.
//
// # Why it exists at all
//
// Reading them was the gap. A ticket today is reachable only through the chat
// conversation that produced it, so answering "what is open in this group" means
// reading a group chat, and answering it across groups means reading several.
// This package is that read and nothing else: a page of rows, and one row.
package ticket

import (
	"context"

	"github.com/google/wire"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/pagination"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
)

// Pagination bounds. They are applied here rather than trusted from the caller:
// an unbounded read of a table that grows with every incident is a query whose
// cost the caller's page size should not be able to set.
const (
	// DefaultPageSize is what a request that names no size gets.
	DefaultPageSize = pagination.DefaultPageSize
	// MaxPageSize is the largest page a caller may ask for.
	MaxPageSize = pagination.MaxPageSize
	// MaxTicketNotes caps the handling log returned for one ticket.
	//
	// It is a cap and not a page because a log is read as a whole — a ticket's
	// /mark entries are a handful, and a caller that had to page through them
	// would be paying for a case that does not happen. The total comes back
	// alongside the rows, so a log that did reach the cap is visible as such.
	MaxTicketNotes = 200
)

// ProviderSet defines Wire DI providers for the ticket biz layer.
var ProviderSet = wire.NewSet(New, wire.Bind(new(TicketBiz), new(*ticketBiz)))

// Filter selects the tickets a page contains. The zero value means "every
// ticket", which is what an operator wants before narrowing anything down.
type Filter struct {
	// Status matches model.TicketStatus values (open/processing/saved/
	// resolved/closed). Empty means any.
	Status string
	// GroupID matches the duty group the ticket is routed to. Empty means any.
	GroupID string
	// AssigneeID matches the primary handler's id. Empty means any.
	AssigneeID string
	// Page is 1-indexed. Values below 1 are read as 1.
	Page int
	// PageSize is clamped to MaxPageSize.
	PageSize int
}

// TicketBiz defines the ticket read surface.
type TicketBiz interface {
	// List returns one page of tickets and the page number that was served.
	List(ctx context.Context, query *api.ListTicketQuery) (*api.ListTicketResponse, error)
	// Get returns one ticket by its ticket id.
	Get(ctx context.Context, req *api.GetTicketRequest) (*model.TicketM, error)
	// ListNote returns one ticket's handling log, oldest first, and the total
	// number of entries it has.
	ListNote(ctx context.Context, req *api.ListTicketNoteRequest) (*api.ListTicketNoteResponse, error)
}

// ticketBiz is the concrete implementation of TicketBiz.
type ticketBiz struct {
	st store.IStore
}

var _ TicketBiz = (*ticketBiz)(nil)

// New constructs a ticketBiz from the persistence layer.
func New(st store.IStore) *ticketBiz {
	return &ticketBiz{st: st}
}

// List returns one page of tickets.
func (b *ticketBiz) List(ctx context.Context, query *api.ListTicketQuery) (*api.ListTicketResponse, error) {
	filter := Filter{
		Status:     query.Status,
		GroupID:    query.GroupID,
		AssigneeID: query.AssigneeID,
		Page:       query.Page,
		PageSize:   query.PageSize,
	}
	page, pageSize := pagination.Page(filter.Page, filter.PageSize)

	// Column names, because that is what the store's filter takes: these are
	// chat.ticket's columns, not the JSON the handler wrote out.
	opts := where.NewWhere().P(page, pageSize)
	if filter.Status != "" {
		opts.F("status", filter.Status)
	}
	if filter.GroupID != "" {
		opts.F("group_id", filter.GroupID)
	}
	if filter.AssigneeID != "" {
		opts.F("assignee_id", filter.AssigneeID)
	}

	total, tickets, err := b.st.Ticket().List(ctx, opts)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	// The page is echoed back rather than left to the caller to remember: a
	// client that sent an out-of-range value would otherwise believe it got
	// that page.
	return &api.ListTicketResponse{
		Total:   total,
		Page:    max(page, 1),
		Tickets: tickets,
	}, nil
}

// Get returns one ticket by id.
//
// A missing row is answered as not-found rather than as an empty ticket: the
// caller asked for something specific, and an empty object would be a value it
// could act on.
func (b *ticketBiz) Get(ctx context.Context, req *api.GetTicketRequest) (*model.TicketM, error) {
	ticketID := req.TicketID
	if ticketID == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("ticket id is required")
	}
	// An equality filter rather than a by-id method: the store's Ticket()
	// interface offers Get with a where.Options, and the column carries a unique
	// index, so this is that lookup.
	obj, err := b.st.Ticket().Get(ctx, where.F("ticket_id", ticketID))
	if err := storeerr.GetError(obj, err, "ticket %q not found", ticketID); err != nil {
		return nil, err
	}
	return obj, nil
}

// ListNote returns one ticket's handling log, oldest first, with the total
// number of entries.
//
// A missing ticket is answered as not-found rather than as an empty log. The
// two are different facts — "this ticket was never marked" and "there is no
// such ticket" — and a caller displaying an empty log for a ticket id it made
// up would be reading the wrong one.
func (b *ticketBiz) ListNote(
	ctx context.Context, req *api.ListTicketNoteRequest,
) (*api.ListTicketNoteResponse, error) {
	ticketID := req.TicketID
	if ticketID == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("ticket id is required")
	}
	// The existence check is the ticket lookup rather than a note count: a
	// ticket with no notes is a valid, empty log.
	obj, err := b.st.Ticket().Get(ctx, where.F("ticket_id", ticketID))
	if err := storeerr.GetError(obj, err, "ticket %q not found", ticketID); err != nil {
		return nil, err
	}
	total, notes, err := b.st.TicketNote().ListByTicket(ctx, ticketID, MaxTicketNotes)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	return &api.ListTicketNoteResponse{TicketID: ticketID, Total: total, Notes: notes}, nil
}
