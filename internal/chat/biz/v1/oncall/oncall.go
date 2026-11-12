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

// Package oncall implements the read surface a downstream consumer polls to
// turn finished tickets into something else — today, OneX publishing them as
// community Q&A.
//
// # Why the filtering is a parameter and not a policy
//
// Which tickets are "ours to publish" is a question about the consumer, not
// about this service: a duty-group roster is shared across business lines, and
// the line that wants to republish its incidents knows which groups are its
// own. So the caller names the groups and the statuses it considers finished,
// and this package answers the one question it does own — "which of those
// tickets have something to say", i.e. at least one handling note — plus the
// material needed to say it.
//
// # Why it hands back tickets rather than a payload
//
// Generating the answer is the consumer's business, including the model call
// that turns a conversation into prose. This package only makes the raw facts
// reachable — the ticket, its handling log, and the group's conversation — so
// that the one place that decides what gets published is not also the place
// that holds the incident.
package oncall

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/wire"
	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/im"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
	chatknown "github.com/onexstack/onexai/pkg/known/chat"
)

// Bounds. The caller's page size is clamped rather than trusted: this surface
// is polled, and an unbounded read of a table that grows with every incident is
// a query whose cost one caller could set for everyone.
const (
	// DefaultLimit is what a poll that names no size gets.
	DefaultLimit = 20
	// MaxLimit is the largest page a caller may ask for.
	MaxLimit = 200

	// TranscriptLimit caps a ticket group's conversation. It is fixed rather
	// than caller-chosen because the platform's own message-list page size caps
	// at 50 — a larger number would be a request the IM backend rejects, not a
	// longer transcript.
	//
	// The consequence is honest and worth stating: a long thread yields its
	// **tail**, not its head. The head is not lost, though — it is the ticket's
	// own Title/Description, which is where the question was written down.
	TranscriptLimit = 50
)

// Provenance of a transcript. The values are part of the read surface's
// contract, so they are constants rather than free-form strings.
const (
	// SourceIM means the chat history surface answered. That surface is itself
	// authoritative-history-first with a cache fallback, and it — not this
	// package — owns which of the two it used.
	SourceIM = "im"
	// SourceArchive means the conversation was read from the archive kept for
	// groups that have been dissolved.
	//
	// It exists because a ticket group is dissolved a day after it closes: past
	// that point the platform will not answer for its messages, and the archive
	// is the only record left. A consumer that got this far is reading a
	// conversation that no longer exists anywhere else.
	SourceArchive = "archive"
	// SourceNone means there was nothing to read: the ticket has no group, or
	// its group has no messages this service knows of.
	//
	// It is distinct from an empty transcript on purpose. "We looked and there
	// is nothing" and "we could not look" produce the same empty list and call
	// for different responses, and a consumer that could not tell them apart
	// would publish an answer built from no material.
	SourceNone = "none"
)

// ProviderSet defines Wire DI providers for the oncall biz layer.
var ProviderSet = wire.NewSet(
	New,
	wire.Bind(new(OncallBiz), new(*oncallBiz)),
	// History is provided by ProvideOncallHistory in the chat package: a
	// wire.Bind has to sit in the same set as the concrete type's provider, and
	// that provider belongs to the chat package's set, which must not have to
	// know this consumer exists. The adapter keeps the dependency one-way.
)

// History is this package's window onto the chat history surface — the same
// consumer-side shape chat.Assets uses, for the same reason.
type History interface {
	// GetHistory returns a conversation's recent messages, oldest first.
	GetHistory(ctx context.Context, convID string, limit int) ([]*im.Message, error)
}

// SyncQuery selects the tickets a poll should look at.
type SyncQuery struct {
	// GroupIDs are the duty groups the caller owns. Required and non-empty: an
	// omitted group filter would silently answer "no tickets", and a caller
	// that had mistyped its roster would read that as "nothing to do".
	GroupIDs []string
	// Statuses are the ticket statuses the caller considers finished. Required
	// and validated against the model's vocabulary, so a typo is a rejected
	// request rather than an empty page.
	Statuses []string
	// Since is a watermark over activity: pass the zero time to take
	// everything.
	Since time.Time
	// Limit is clamped to MaxLimit.
	Limit int
}

// Candidate is one ticket a poll should look at, with its handling log.
type Candidate struct {
	Ticket *model.TicketM
	// The ticket carries the closing summary a consumer publishes; there is nothing
	// else to hand over. Its handling log used to travel here too, back when the
	// consumer wrote its own write-up and needed the material — now the ticket's
	// own row has everything, and a field nobody reads is a field the next reader
	// has to work out the purpose of.
	// Eligible reports whether this ticket still meets the conditions the query
	// was asked under: one of the named statuses, and at least one handling note.
	//
	// It is always true for a page from ListForSync, which selects on exactly
	// that. It is a field rather than an assumption because Ticket returns one
	// that may not be: a consumer that is re-reading a ticket it has already
	// acted on needs to know whether it may still publish it, and a ticket that
	// was reopened has to be told apart from one that is simply done.
	Eligible bool
}

// Transcript is one ticket group's conversation and where it came from.
type Transcript struct {
	// ConvID is the Lark chat id of the ticket group. Empty when the ticket has
	// no associated conversation.
	ConvID string
	// Messages are the conversation, oldest first.
	Messages []*im.Message
	// Source is one of SourceIM / SourceArchive / SourceNone.
	Source string
}

// OncallBiz defines the read surface for a downstream republisher.
type OncallBiz interface {
	// ListForSync returns the tickets matching the query, oldest activity
	// first, each with its handling log.
	ListForSync(ctx context.Context, query SyncQuery) ([]*Candidate, error)
	// Ticket returns one ticket with its handling log, whatever its status, and
	// reports whether it still meets the given statuses.
	//
	// # Why a consumer needs this and not only the list
	//
	// A pass that walks a watermark moves past a ticket it could not finish — it
	// has to, or one broken ticket would hold the watermark back and every later
	// one would be re-read forever. The retry therefore starts from the
	// consumer's own record of what it owes, which names a ticket and nothing
	// else. Reading it back needs this call, and so does the question that comes
	// with it: is this still something to publish, or was it reopened while we
	// were failing to summarize it.
	Ticket(ctx context.Context, ticketID string, statuses []string) (*Candidate, error)
	// Transcript returns one ticket group's conversation.
	Transcript(ctx context.Context, ticketID string) (*Transcript, error)
	// ListForSyncView is the REST wire shape of ListForSync.
	ListForSyncView(ctx context.Context, req *api.ListOncallTicketRequest, appID string) (*api.ListOncallTicketResponse, error)
	// TicketView is the REST wire shape of Ticket.
	TicketView(ctx context.Context, req *api.GetOncallTicketRequest, appID string) (*api.GetOncallTicketResponse, error)
}

// oncallBiz is the concrete implementation of OncallBiz.
type oncallBiz struct {
	st      store.IStore
	history History
}

var _ OncallBiz = (*oncallBiz)(nil)

// New constructs an oncallBiz from the persistence layer and the chat history
// surface.
func New(st store.IStore, history History) *oncallBiz {
	return &oncallBiz{st: st, history: history}
}

// ListForSync returns the tickets matching the query, each with its handling
// log, ordered by activity.
func (b *oncallBiz) ListForSync(ctx context.Context, query SyncQuery) ([]*Candidate, error) {
	if len(query.GroupIDs) == 0 {
		return nil, errno.ErrInvalidArgument.WithMessage("groups is required and must name at least one duty group")
	}
	if err := requireStatuses(query.Statuses); err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit < 1 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	tickets, err := b.st.Ticket().ListForSync(ctx, query.GroupIDs, query.Statuses, query.Since, limit)
	if err != nil {
		return nil, storeerr.Read(err)
	}

	out := make([]*Candidate, 0, len(tickets))
	for _, tk := range tickets {
		// One row per ticket and no second query: everything a consumer needs is
		// on the ticket, which is the point of the summary living there. The page
		// used to cost one extra read per candidate to fetch the handling log.
		out = append(out, &Candidate{
			Ticket: tk,
			// The query selected on this, so it is true by construction — written
			// rather than assumed so that the two reads answer the same shape.
			Eligible: true,
		})
	}
	return out, nil
}

// Ticket returns one ticket with its handling log, whatever its status.
//
// # Why it does not narrow on the status
//
// Because the caller is asking a question that a narrowed read cannot answer:
// "is this ticket still something to publish". A lookup that returned
// not-found for a reopened ticket would tell the caller the same thing it tells
// it about a ticket that never existed, and the two call for opposite actions —
// one is a publication to withdraw, the other is nothing to do.
func (b *oncallBiz) Ticket(ctx context.Context, ticketID string, statuses []string) (*Candidate, error) {
	if ticketID == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("ticket id is required")
	}
	if err := requireStatuses(statuses); err != nil {
		return nil, err
	}

	tk, err := b.st.Ticket().Get(ctx, where.F("ticket_id", ticketID))
	if err != nil {
		if errors.Is(err, store.ErrRecordNotFound) {
			return nil, errno.ErrNotFound.WithMessage("ticket %q not found", ticketID)
		}
		return nil, storeerr.Read(err)
	}

	return &Candidate{
		Ticket:   tk,
		Eligible: eligible(tk, statuses),
	}, nil
}

// eligible is the same condition ListForSync selects on, spelled once more
// because one of the two reads applies it in SQL and the other in Go.
//
// They have to agree, and the place they would disagree is the summary test: the
// query asks for a non-empty summary that is not the brief marker, and this asks
// the loaded row. A test pins the pair — see TestTicketAgreesWithListForSync.
func eligible(tk *model.TicketM, statuses []string) bool {
	// Exact equality with the marker, matching the query's SQL — see
	// known/chat.CanonicalSummary for why the tolerance is not here. A value that
	// somehow arrived un-canonicalised would be treated as a write-up by both sides,
	// which is the same answer from both, and that is what matters.
	if tk.Summary == chatknown.SummaryBrief || strings.TrimSpace(tk.Summary) == "" {
		return false
	}
	for _, s := range statuses {
		if string(tk.Status) == s {
			return true
		}
	}
	return false
}

// requireStatuses validates the caller's idea of "finished".
//
// Required and checked rather than defaulted: the empty list would select
// nothing, and an unknown name would select nothing, and both arrive as an empty
// page — which a caller reads as "there is nothing to do". A rejected request is
// the only answer that cannot be mistaken for that.
func requireStatuses(statuses []string) error {
	if len(statuses) == 0 {
		return errno.ErrInvalidArgument.WithMessage("statuses is required and must name at least one ticket status")
	}
	for _, s := range statuses {
		if !model.IsTicketStatus(s) {
			return errno.ErrInvalidArgument.WithMessage(
				"unknown status %q; accepted statuses are %v", s, model.TicketStatuses())
		}
	}
	return nil
}

// Transcript returns one ticket group's conversation.
//
// # The order of the two reads is the whole of it
//
// The live surface first, because for the day after a ticket closes the group
// still exists and the platform is the only complete source — it holds the
// bot's own messages, which nothing here caches. The archive second, because
// past that day the group is gone and the archive is all that is left.
//
// A ticket with no associated session answers SourceNone rather than an error:
// a ticket that never got a group is a fact about that ticket, and a consumer
// polling a page of them should not have the whole page fail on it.
func (b *oncallBiz) Transcript(ctx context.Context, ticketID string) (*Transcript, error) {
	// Trimmed rather than only compared to "": a blank id would otherwise be
	// looked up, miss, and be answered as SourceNone — turning a malformed
	// argument into the same answer as "this ticket never had a group".
	ticketID = strings.TrimSpace(ticketID)
	if ticketID == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("ticket id is required")
	}

	// The session row outlives the group: dissolving marks it dismissed, it does
	// not delete it. So the lookup answers for a live group and a gone one alike,
	// which is what lets the archive be found at all.
	sess, err := b.st.Session().Get(ctx, where.F("associated_ticket_id", ticketID))
	if err != nil && !errors.Is(err, store.ErrRecordNotFound) {
		return nil, storeerr.Read(err)
	}
	if sess == nil || sess.SessionID == "" {
		return &Transcript{Source: SourceNone}, nil
	}

	if b.history != nil {
		msgs, err := b.history.GetHistory(ctx, sess.SessionID, TranscriptLimit)
		if err != nil {
			return nil, storeerr.Read(err)
		}
		if len(msgs) > 0 {
			return &Transcript{ConvID: sess.SessionID, Messages: msgs, Source: SourceIM}, nil
		}
	}

	archived, err := b.st.ChatMessages().ListByGroup(ctx, sess.SessionID, TranscriptLimit)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if len(archived) == 0 {
		return &Transcript{ConvID: sess.SessionID, Source: SourceNone}, nil
	}

	msgs := make([]*im.Message, 0, len(archived))
	for _, m := range archived {
		createTime := ptr.From(m.Time)
		msgs = append(msgs, &im.Message{
			ID:         m.MessageID,
			ConvID:     m.GroupID,
			SenderID:   m.SenderID,
			SenderName: m.SenderName,
			Content:    im.MessageContent{Type: "text", Text: m.Content},
			CreateTime: createTime,
		})
	}
	return &Transcript{ConvID: sess.SessionID, Messages: msgs, Source: SourceArchive}, nil
}
