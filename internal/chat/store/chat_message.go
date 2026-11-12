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
	"gorm.io/gorm/clause"

	"github.com/onexstack/onexai/internal/chat/model"
)

// ChatMessageStore is the archive of messages from groups that no longer exist.
//
// # Why an archive at all, when the message cache already holds them
//
// The cache is a cache: it is keyed by message for the search and history
// surfaces, and nothing promises it outlives the conversation. A dissolved
// group takes its conversation with it — the platform will not answer for its
// messages any more — so whatever is to be kept has to be copied out before
// that happens. This is that copy, and it is the only record left afterwards.
type ChatMessageStore interface {
	Create(ctx context.Context, obj *model.ChatMessageM) error
	Update(ctx context.Context, obj *model.ChatMessageM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.ChatMessageM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.ChatMessageM, error)

	ChatMessageExpansion
}

// ChatMessageExpansion holds the archive-specific operations: an idempotent
// bulk copy and a newest-N read.
type ChatMessageExpansion interface {
	// Archive writes the messages of one group, ignoring the ones already
	// archived.
	//
	// Idempotent by message id, and it has to be: the sweep that calls this can
	// be interrupted between the copy and the mark that says it happened, and a
	// second pass must be able to finish the job rather than fail on its own
	// earlier work.
	Archive(ctx context.Context, msgs []*model.ChatMessageM) (int, error)

	// ListByGroup returns the most recent `limit` archived messages of one
	// group, oldest first — the same shape GetHistory answers in, so a caller
	// can read one and then the other without reordering anything.
	//
	// It returns the newest N rather than the first N because a conversation is
	// read backwards from where it ended: the setup that matters is at the
	// tail. Within the page the order is chronological, so a reader that got
	// this far does not have to know that.
	ListByGroup(ctx context.Context, groupID string, limit int) ([]*model.ChatMessageM, error)
}

type chatMessageStore struct {
	*nilSafeStore[model.ChatMessageM]
	db *gorm.DB
}

var _ ChatMessageStore = (*chatMessageStore)(nil)

func newChatMessageStore(s *store) *chatMessageStore {
	return &chatMessageStore{nilSafeStore: newNilSafeStore[model.ChatMessageM](s), db: s.db}
}

// Archive implements ChatMessageStore.
func (s *chatMessageStore) Archive(ctx context.Context, msgs []*model.ChatMessageM) (int, error) {
	if s.db == nil || len(msgs) == 0 {
		return 0, nil
	}
	// A conflicting insert is a message this sweep already wrote — an earlier
	// pass that was interrupted before it could mark the group. DoNothing makes
	// the second attempt a no-op for those rows instead of an error, which is
	// what lets the job simply be run again.
	res := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "message_id"}}, DoNothing: true}).
		CreateInBatches(msgs, 200)
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// ListByGroup implements ChatMessageStore.
func (s *chatMessageStore) ListByGroup(ctx context.Context, groupID string, limit int) ([]*model.ChatMessageM, error) {
	if s.db == nil || groupID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultChatMessageLimit
	}

	var msgs []*model.ChatMessageM
	// The ordering key is coalesced rather than `time` alone: `time` is
	// nullable (the platform did not always say when a message was sent) and
	// the two dialects disagree about where a NULL lands in a DESC sort, so a
	// bare `time DESC` would return a different page on each. Falling back to
	// created_at — the moment the copy was taken — keeps the order stable and
	// puts an undated message where it was archived.
	err := s.db.WithContext(ctx).
		Where("group_id = ?", groupID).
		Order("COALESCE(time, created_at) DESC").
		Limit(limit).
		Find(&msgs).Error
	if err != nil {
		return nil, err
	}
	// Newest-first was asked for only to bound the page; callers read it
	// forwards.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// defaultChatMessageLimit bounds a read that names no size. It matches the
// chat history surface's own default (defaultHistoryLimit in biz/v1/chat) so
// the archive answers the same shape as the live path it stands in for.
const defaultChatMessageLimit = 50
