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

	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/model"
)

// TicketNoteStore defines the standard store for a ticket's handling log.
type TicketNoteStore interface {
	Create(ctx context.Context, obj *model.TicketNoteM) error
	Update(ctx context.Context, obj *model.TicketNoteM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.TicketNoteM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.TicketNoteM, error)

	TicketNoteExpansion
}

// TicketNoteExpansion holds the log-shaped read the handlers use.
type TicketNoteExpansion interface {
	// ListByTicket returns a ticket's notes in the order they were written,
	// oldest first, together with the total number of notes the ticket has.
	//
	// limit bounds the rows returned; a non-positive limit returns all of them.
	// The total is counted regardless of the limit, so a caller can tell a
	// trimmed log from a complete one.
	ListByTicket(ctx context.Context, ticketID string, limit int) (int64, []*model.TicketNoteM, error)
}

// ticketNoteStore implements TicketNoteStore over the generic onexstack store.
// It holds the raw DB because the generic List orders newest-first, and a
// handling log reads oldest-first.
type ticketNoteStore struct {
	*nilSafeStore[model.TicketNoteM]
	db *gorm.DB
}

var _ TicketNoteStore = (*ticketNoteStore)(nil)

// newTicketNoteStore creates a new ticketNoteStore backed by the given store.
func newTicketNoteStore(s *store) *ticketNoteStore {
	return &ticketNoteStore{
		nilSafeStore: newNilSafeStore[model.TicketNoteM](s),
		db:           s.db,
	}
}

// ListByTicket returns one ticket's notes oldest-first, and how many it has.
//
// An empty ticket id yields no rows rather than every row: the caller asked
// for a particular ticket's log, and a scan of the whole table would be the
// opposite of that.
//
// The count and the page are two statements rather than one Count-then-Find on
// the same *gorm.DB, because Count rewrites the statement it runs on.
func (s *ticketNoteStore) ListByTicket(ctx context.Context, ticketID string, limit int) (int64, []*model.TicketNoteM, error) {
	if ticketID == "" {
		return 0, nil, nil
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&model.TicketNoteM{}).
		Where("ticket_id = ?", ticketID).
		Count(&total).Error; err != nil {
		return 0, nil, err
	}

	query := s.db.WithContext(ctx).Where("ticket_id = ?", ticketID).Order("id asc")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var notes []*model.TicketNoteM
	if err := query.Find(&notes).Error; err != nil {
		return 0, nil, err
	}
	return total, notes, nil
}
