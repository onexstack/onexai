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
	"gorm.io/gorm/clause"

	"github.com/onexstack/onexai/internal/chat/model"
)

// peerAgentUpsertColumns are the columns a conflicting write updates. The key
// columns are absent on purpose: a conflict means "this peer already has a
// binding", and rewriting its identity from the incoming row would be the same
// values anyway.
//
// `updated_at` is included, which is what makes a repeated push idempotent in
// the clock sense too — the row carries the time of its last successful write
// rather than of its first.
var peerAgentUpsertColumns = []string{"canonical", "agents", "source", "updated_at"}

// PeerAgentStore defines the standard persistence API for chat.peer_agent plus
// the triple-keyed operations that are its reason to exist.
type PeerAgentStore interface {
	Create(ctx context.Context, obj *model.PeerAgentM) error
	Update(ctx context.Context, obj *model.PeerAgentM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.PeerAgentM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.PeerAgentM, error)

	PeerAgentExpansion
}

// PeerAgentExpansion holds the binding-shaped operations.
type PeerAgentExpansion interface {
	// GetByPeer returns the binding for one peer, or nil when there is none.
	//
	// A miss is (nil, nil) rather than an error: "this peer has no binding" is
	// the ordinary state for everyone who is not a OneX user, and every caller
	// has to tell it apart from a lookup that failed.
	GetByPeer(ctx context.Context, backend, scope, peerID string) (*model.PeerAgentM, error)

	// Put upserts a binding by (backend, scope, peer_id).
	Put(ctx context.Context, m *model.PeerAgentM) error

	// DeleteByPeer removes a binding. Deleting one that is not there is not an
	// error: unbinding twice is the same outcome as unbinding once.
	DeleteByPeer(ctx context.Context, backend, scope, peerID string) error
}

// peerAgentStore is the concrete PeerAgentStore implementation.
type peerAgentStore struct {
	*nilSafeStore[model.PeerAgentM]
	db *gorm.DB
}

var _ PeerAgentStore = (*peerAgentStore)(nil)

// newPeerAgentStore creates a peerAgentStore backed by the given store.
func newPeerAgentStore(s *store) *peerAgentStore {
	return &peerAgentStore{nilSafeStore: newNilSafeStore[model.PeerAgentM](s), db: s.db}
}

// GetByPeer implements PeerAgentStore.
func (s *peerAgentStore) GetByPeer(ctx context.Context, backend, scope, peerID string) (*model.PeerAgentM, error) {
	if s.db == nil || backend == "" || peerID == "" {
		return nil, nil
	}
	var rec model.PeerAgentM
	err := s.db.WithContext(ctx).
		Where("backend = ? AND scope = ? AND peer_id = ?", backend, scope, peerID).
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Put implements PeerAgentStore.
//
// The conflict is on the triple, so a second push for the same peer updates the
// row instead of inserting a duplicate. `updated_at` is read from the clock here
// rather than from the row, because a caller that omits it means "now" and not
// "the epoch" — the same judgement the agent resource's writers make.
func (s *peerAgentStore) Put(ctx context.Context, m *model.PeerAgentM) error {
	if s.db == nil || m == nil || m.Backend == "" || m.PeerID == "" {
		return nil
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = time.Now()
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			// The triple, spelled as the model's unique index. MySQL ignores it
			// — a duplicate key on any unique index triggers the update — but
			// the other dialects need it named.
			Columns: []clause.Column{
				{Name: "backend"}, {Name: "scope"}, {Name: "peer_id"},
			},
			DoUpdates: clause.AssignmentColumns(peerAgentUpsertColumns),
		}).
		Create(m).Error
}

// DeleteByPeer implements PeerAgentStore.
func (s *peerAgentStore) DeleteByPeer(ctx context.Context, backend, scope, peerID string) error {
	if s.db == nil || backend == "" || peerID == "" {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("backend = ? AND scope = ? AND peer_id = ?", backend, scope, peerID).
		Delete(&model.PeerAgentM{}).Error
}
