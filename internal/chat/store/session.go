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
	"time"

	storelogger "github.com/onexstack/onexstack/pkg/logger/slog/store"
	genericstore "github.com/onexstack/onexstack/pkg/store"
	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/model"
)

// SessionStore defines the interface for managing session-related data operations.
type SessionStore interface {
	// Create inserts a new Session record into the store.
	Create(ctx context.Context, obj *model.SessionM) error

	// Update modifies an existing Session record in the store based on the given model.
	Update(ctx context.Context, obj *model.SessionM) error

	// Delete removes Session records matching the specified criteria.
	Delete(ctx context.Context, opts *where.Options) error

	// Get retrieves a single Session record matching the specified criteria.
	Get(ctx context.Context, opts *where.Options) (*model.SessionM, error)

	// List retrieves one page of sessions and the total count matching opts.
	List(ctx context.Context, opts *where.Options) (int64, []*model.SessionM, error)

	// SessionExpansion is a placeholder for extension methods for sessions,
	// to be implemented by additional interfaces if needed.
	SessionExpansion
}

// SessionExpansion is an empty interface provided for extending
// the SessionStore interface.
type SessionExpansion interface {
	// CountByGroup counts tickets in the given duty group. The group id lives on
	// the ticket table, used by the round-robin assignment strategy.
	CountByGroup(ctx context.Context, groupID string) (int64, error)

	// SetRefs records what a conversation declares it wants.
	//
	// # Why this is not Session().Update
	//
	// The caller holds a SessionM it loaded at the top of the turn, and a
	// full-row Update would write every column back from that snapshot — while
	// another request may have changed something in between. That is a lost
	// update, and it is silent: the other write simply disappears.
	//
	// So this touches one column. It is unconditional because it runs when a
	// human changes their mind — a few times per conversation at most, not once
	// per message.
	SetRefs(ctx context.Context, sessionID string, refs string) error

	// ListDismissable returns the group conversations that are due to be
	// dissolved, oldest first.
	//
	// # What "due" means, and why every clause is load-bearing
	//
	// closed_at is not null: a conversation nobody closed is still in use, and
	// dissolving it would take a group away from people using it.
	//
	// saved_at is null: /save is a person saying "keep this one". It is the only
	// way to opt out, so a sweep that ignored it would dissolve exactly the
	// groups somebody deliberately preserved.
	//
	// dismissed_at is null: this is the idempotence. A pass that dissolved a
	// group but was interrupted before it could say so would otherwise dissolve
	// it again on every run.
	//
	// The limit bounds one pass. The table is small and a pass is a minute, so a
	// backlog drains over several passes rather than in one long transaction.
	ListDismissable(ctx context.Context, closedBefore time.Time, limit int) ([]*model.SessionM, error)

	// MarkDismissed records that a group was dissolved.
	//
	// One column, written only when it is still empty, so two replicas sweeping
	// at once cannot both believe they were the one that dissolved it.
	MarkDismissed(ctx context.Context, sessionID string, at time.Time) error
}

// sessionStore implements the SessionStore interface over the generic onexstack
// store. It also holds the raw DB so expansion methods can query related tables.
type sessionStore struct {
	*genericstore.Store[model.SessionM]
	db *gorm.DB
}

// Ensure that sessionStore satisfies the SessionStore interface at compile time.
var _ SessionStore = (*sessionStore)(nil)

// newSessionStore creates a new sessionStore instance backed by the given store.
func newSessionStore(s *store) *sessionStore {
	return &sessionStore{
		Store: genericstore.NewStore[model.SessionM](s, storelogger.NewLogger()),
		db:    s.db,
	}
}

// SetRefs implements SessionExpansion.
func (s *sessionStore) SetRefs(ctx context.Context, sessionID string, refs string) error {
	if s.db == nil || sessionID == "" {
		return nil
	}
	return s.db.WithContext(ctx).
		Model(&model.SessionM{}).
		Where("session_id = ?", sessionID).
		Update("refs", refs).Error
}

// CountByGroup counts tickets in the given duty group. The group id lives on
// the ticket table, so the session store queries the ticket model directly
// (used by the round-robin assignment strategy).
// ListDismissable implements SessionExpansion.
func (s *sessionStore) ListDismissable(ctx context.Context, closedBefore time.Time, limit int) ([]*model.SessionM, error) {
	if s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	var rows []*model.SessionM
	if err := s.db.WithContext(ctx).
		Where("closed_at IS NOT NULL AND closed_at <= ?", closedBefore).
		Where("saved_at IS NULL AND dismissed_at IS NULL").
		Order("closed_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// MarkDismissed implements SessionExpansion.
func (s *sessionStore) MarkDismissed(ctx context.Context, sessionID string, at time.Time) error {
	if s.db == nil || sessionID == "" {
		return nil
	}
	// The `dismissed_at IS NULL` predicate makes this a claim rather than a
	// write: two replicas sweeping at once, one of them updates a row.
	return s.db.WithContext(ctx).
		Model(&model.SessionM{}).
		Where("session_id = ? AND dismissed_at IS NULL", sessionID).
		Update("dismissed_at", at).Error
}

func (s *sessionStore) CountByGroup(ctx context.Context, groupID string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.TicketM{}).
		Where("group_id = ?", groupID).
		Count(&count).Error
	return count, err
}
