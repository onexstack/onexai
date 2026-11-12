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

	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/model"
)

// MessageStore is the standard store for the message cache plus the reads its
// callers actually use.
type MessageStore interface {
	Create(ctx context.Context, obj *model.MessageM) error
	Update(ctx context.Context, obj *model.MessageM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.MessageM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.MessageM, error)

	MessageExpansion
}

// MessageExpansion holds the cache-specific reads. They keep their historic
// shape because each encodes an ordering the generic List cannot express.
type MessageExpansion interface {
	Save(ctx context.Context, m *model.MessageM) error
	ListByConv(ctx context.Context, convID string, limit int) ([]*model.MessageM, error)
	Search(ctx context.Context, query string, limit int) ([]*model.MessageM, error)
	GetByMsgID(ctx context.Context, msgID string) (*model.MessageM, error)
}

// messageStore is the concrete MessageStore implementation.
type messageStore struct {
	*nilSafeStore[model.MessageM]
	db *gorm.DB
}

var _ MessageStore = (*messageStore)(nil)

// newMessageStore creates a messageStore backed by the given store.
func newMessageStore(s *store) *messageStore {
	return &messageStore{nilSafeStore: newNilSafeStore[model.MessageM](s), db: s.db}
}

// Save persists a message record. Ignores duplicate MsgIDs. No-op when no DB.
func (s *messageStore) Save(ctx context.Context, m *model.MessageM) error {
	if s.db == nil {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("msg_id = ?", m.MsgID).
		FirstOrCreate(m).Error
}

// ListByConv returns the most recent `limit` messages for the given
// conversation, ordered oldest-first.
func (s *messageStore) ListByConv(ctx context.Context, convID string, limit int) ([]*model.MessageM, error) {
	if s.db == nil {
		return nil, nil
	}
	var records []*model.MessageM
	err := s.db.WithContext(ctx).
		Where("conv_id = ?", convID).
		Order("send_time DESC").
		Limit(limit).
		Find(&records).Error
	if err != nil {
		return nil, err
	}
	// Reverse to chronological order.
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	return records, nil
}

// GetByMsgID returns the cached message record for the given IM-system message
// ID, or nil (no error) when not found or no DB is configured.
func (s *messageStore) GetByMsgID(ctx context.Context, msgID string) (*model.MessageM, error) {
	if s.db == nil || msgID == "" {
		return nil, nil
	}
	var rec model.MessageM
	err := s.db.WithContext(ctx).Where("msg_id = ?", msgID).First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &rec, nil
}

// Search returns cached messages whose content matches query (case-insensitive
// substring), newest first, across all conversations.
func (s *messageStore) Search(ctx context.Context, query string, limit int) ([]*model.MessageM, error) {
	if s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	var records []*model.MessageM
	err := s.db.WithContext(ctx).
		Where("content LIKE ?", "%"+query+"%").
		Order("send_time DESC").
		Limit(limit).
		Find(&records).Error
	if err != nil {
		return nil, err
	}
	return records, nil
}
