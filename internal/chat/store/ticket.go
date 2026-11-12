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

package store

import (
	"context"
	"fmt"
	"time"

	storelogger "github.com/onexstack/onexstack/pkg/logger/slog/store"
	genericstore "github.com/onexstack/onexstack/pkg/store"
	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/model"
	chatknown "github.com/onexstack/onexai/pkg/known/chat"
)

// TicketStore defines the interface for managing ticket-related data operations.
type TicketStore interface {
	// Create inserts a new Ticket record into the store.
	Create(ctx context.Context, obj *model.TicketM) error

	// Update modifies an existing Ticket record in the store based on the given model.
	Update(ctx context.Context, obj *model.TicketM) error

	// Delete removes Ticket records matching the specified criteria.
	Delete(ctx context.Context, opts *where.Options) error

	// Get retrieves a single Ticket record matching the specified criteria.
	Get(ctx context.Context, opts *where.Options) (*model.TicketM, error)

	// List returns one page of tickets together with the total number of rows
	// matching the given criteria, so a caller can paginate without loading
	// every ticket into memory.
	//
	// The SQL implementation inherits this from the generic store it embeds;
	// it is named here to put it on the interface the read surface uses.
	List(ctx context.Context, opts *where.Options) (int64, []*model.TicketM, error)

	// TicketExpansion is a placeholder for extension methods for tickets,
	// to be implemented by additional interfaces if needed.
	TicketExpansion
}

// TicketExpansion is an empty interface provided for extending
// the TicketStore interface.
type TicketExpansion interface {
	// CountByGroup counts tickets in the given duty group.
	CountByGroup(ctx context.Context, groupID string) (int64, error)

	// ListForSync returns the tickets a downstream reader should look at, oldest
	// first. It exists for the OneX on-call sync — see the implementation for
	// what "a candidate" means.
	//
	// limit follows this package's convention: zero or less means no bound.
	ListForSync(ctx context.Context, groupIDs, statuses []string, since time.Time, limit int) ([]*model.TicketM, error)

	// UpdateSummary writes a ticket's closing summary, and reports whether this
	// call is the one that wrote it.
	//
	// # Why this is not Ticket().Update
	//
	// Update saves the whole row from a copy the caller loaded earlier, so it
	// writes back every column it read — including the ones another command
	// changed in between. For a summary that would be a lost update on status,
	// level or assignee, silently. It also goes through the Regen mirror, which
	// pushes before it writes: a Regen hiccup would then fail the local
	// summary write, so a ticket would sit summarized nowhere while the
	// operator saw a failure about the incident platform. This names the two
	// columns it owns and nothing else.
	//
	// # Why the write is conditional
	//
	// `WHERE summary = ''` makes the first writer the only writer, which is what
	// keeps a second /close — or the same one arriving twice — from paying for a
	// second model run. It is also the strongest guarantee available without a
	// lock: the database decides, so two processes cannot both win. A `false`
	// answer is not an error; it means somebody got there first.
	//
	// at is written explicitly rather than left to the column default. The
	// downstream sync walks a watermark over updated_at, so a summary write that
	// did not move that column would be a summary nobody ever reads.
	UpdateSummary(ctx context.Context, ticketID, summary string, at time.Time) (bool, error)
}

// ticketStore implements the TicketStore interface over the generic onexstack
// store. It also holds the raw DB so expansion methods can query related tables.
type ticketStore struct {
	*genericstore.Store[model.TicketM]
	db *gorm.DB
}

// Ensure that ticketStore satisfies the TicketStore interface at compile time.
var _ TicketStore = (*ticketStore)(nil)

// newTicketStore creates a new ticketStore instance backed by the given store.
func newTicketStore(s *store) *ticketStore {
	return &ticketStore{
		Store: genericstore.NewStore[model.TicketM](s, storelogger.NewLogger()),
		db:    s.db,
	}
}

// CountByGroup counts tickets in the given group via a SQL COUNT instead of
// loading every matching row into memory.
func (s *ticketStore) CountByGroup(ctx context.Context, groupID string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.TicketM{}).
		Where("group_id = ?", groupID).
		Count(&count).Error
	return count, err
}

// UpdateSummary writes a ticket's closing summary if nobody has yet.
//
// The predicate carries two conditions and both are load-bearing: the ticket id
// names the row, and `summary = ”` names the state this write is allowed to
// move from. A row that already has a summary matches nothing, which is how the
// caller learns that somebody got there first without a lock and without a read.
//
// updated_at is written rather than left alone even though this is not a change
// to the ticket's own state: the downstream sync walks a watermark over that
// column, so a summary that did not move it is a summary nobody ever reads. See
// the interface for the rest.
func (s *ticketStore) UpdateSummary(ctx context.Context, ticketID, summary string, at time.Time) (bool, error) {
	if ticketID == "" || summary == "" {
		// Refused rather than written: an empty summary is the state this
		// function moves *away* from, so writing one would leave the row exactly
		// as it was while reporting success — and the caller's "did I write it"
		// would be a yes about a write that changed nothing.
		return false, fmt.Errorf("ticket summary: a ticket id and a non-empty summary are both required")
	}

	res := s.db.WithContext(ctx).Model(&model.TicketM{}).
		Where("ticket_id = ?", ticketID).
		Where("summary = ?", "").
		Updates(map[string]any{"summary": summary, "updated_at": at})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ListForSync returns the tickets a downstream reader should look at, oldest
// first.
//
// # What makes a ticket a candidate
//
// Three conditions, and the caller supplies the first two: the ticket is in one
// of the named duty groups, it is in one of the named statuses — and it carries a
// **closing summary that is a write-up**.
//
// # Why the summary is the gate
//
// A ticket's summary is written when the ticket closes, so the question "is this
// worth republishing" is answered by the model that wrote it: a ticket it declined
// to write up carries the brief marker instead of a summary, and the marker is
// excluded here. The constant lives in the shared-vocabulary package because the
// prompt asks for it in those exact words — a query excluding a different spelling
// would publish precisely the tickets the model said to skip, quietly.
//
// # Why the watermark is a plain updated_at
//
// It can be, now. A summary write moves updated_at (see UpdateSummary), and
// /mark's notes no longer matter to this reader: a ticket's write-up belongs to
// the ticket rather than to its log, so an entry added after the close is not a
// reason to republish. That is what lets the note table leave this query entirely —
// no EXISTS, no derived activity, no second index to keep in step.
//
// since is a watermark, not a filter: pass the zero time to take everything.
func (s *ticketStore) ListForSync(
	ctx context.Context,
	groupIDs, statuses []string,
	since time.Time,
	limit int,
) ([]*model.TicketM, error) {
	tickets := model.TableNameTicketM()

	var rows []*model.TicketM
	query := s.db.WithContext(ctx).
		Model(&model.TicketM{}).
		Where(fmt.Sprintf("%s.group_id IN ?", tickets), groupIDs).
		Where(fmt.Sprintf("%s.status IN ?", tickets), statuses).
		Where(fmt.Sprintf("%s.summary <> ?", tickets), "").
		// Not "" — a write-up. The model declines a ticket it does not consider
		// worth recording by answering with this sentence, and those answers are
		// excluded here rather than published as questions with nothing under them.
		Where(fmt.Sprintf("%s.summary <> ?", tickets), chatknown.SummaryBrief).
		Where(fmt.Sprintf("%s.updated_at > ?", tickets), since).
		Order(fmt.Sprintf("%s.updated_at ASC", tickets))
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
