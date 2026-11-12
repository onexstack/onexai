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
	"errors"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/model"
)

// TicketEscalationStore persists the runtime state of ticket escalation.
type TicketEscalationStore interface {
	Create(ctx context.Context, obj *model.TicketEscalationM) error
	Update(ctx context.Context, obj *model.TicketEscalationM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.TicketEscalationM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.TicketEscalationM, error)

	TicketEscalationExpansion
}

// TicketEscalationExpansion carries the worker's queries.
type TicketEscalationExpansion interface {
	// GetByTicket returns the state for a ticket, or (nil, nil) when none
	// exists.
	GetByTicket(ctx context.Context, ticketID string) (*model.TicketEscalationM, error)
	// ListDue returns active states whose next attempt is due. Rows are
	// conditional-updated by the worker, so a concurrent replica that loses the
	// update is simply skipped on its next pass.
	ListDue(ctx context.Context, now int64, limit int) ([]*model.TicketEscalationM, error)
	// ClaimDue marks a row as being handled by this replica. The conditional
	// update is the multi-replica guard.
	ClaimDue(ctx context.Context, id uint64, now, next int64) (bool, error)
}

type ticketEscalationStore struct {
	*nilSafeStore[model.TicketEscalationM]
	db *gorm.DB
}

var _ TicketEscalationStore = (*ticketEscalationStore)(nil)

func newTicketEscalationStore(s *store) *ticketEscalationStore {
	return &ticketEscalationStore{
		nilSafeStore: newNilSafeStore[model.TicketEscalationM](s),
		db:           s.db,
	}
}

func (s *ticketEscalationStore) GetByTicket(
	ctx context.Context, ticketID string,
) (*model.TicketEscalationM, error) {
	if s.db == nil || ticketID == "" {
		return nil, nil
	}
	var row model.TicketEscalationM
	err := s.db.WithContext(ctx).Where("ticket_id = ?", ticketID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *ticketEscalationStore) ListDue(
	ctx context.Context, now int64, limit int,
) ([]*model.TicketEscalationM, error) {
	if s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	var rows []*model.TicketEscalationM
	err := s.db.WithContext(ctx).
		Where("status IN ? AND next_attempt_at <= ?",
			[]string{model.EscalationPending, model.EscalationNotified}, now).
		Order("next_attempt_at asc").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

func (s *ticketEscalationStore) ClaimDue(
	ctx context.Context, id uint64, now, next int64,
) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	result := s.db.WithContext(ctx).
		Model(&model.TicketEscalationM{}).
		Where("id = ? AND status IN ? AND next_attempt_at <= ?",
			id, []string{model.EscalationPending, model.EscalationNotified}, now).
		Updates(map[string]any{
			"next_attempt_at": next,
			"updated_at":      time.Now().UnixMilli(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}
