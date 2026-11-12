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

package mem

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	agent "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// PGStore is the durable conversation history. It implements the same
// interface as the Redis store so the runtime does not branch on where history
// lives; Redis remains available as a cache, but this row set is the source.
type PGStore struct {
	Runs runstore.Store
}

var _ SessionStore = (*PGStore)(nil)

// NewPGStore returns a durable session store over runs.
func NewPGStore(runs runstore.Store) SessionStore { return &PGStore{Runs: runs} }

// Load implements SessionStore.
func (s *PGStore) Load(ctx context.Context, key string) ([]agent.Message, error) {
	rows, err := s.Runs.ListMessage(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]agent.Message, 0, len(rows))
	for _, row := range rows {
		images, err := decodeImages(row.Images)
		if err != nil {
			return nil, err
		}
		out = append(out, agent.Message{Role: row.Role, Content: row.Content, Images: images})
	}
	return out, nil
}

// Append implements SessionStore.
func (s *PGStore) Append(ctx context.Context, key string, message agent.Message) error {
	images, err := encodeImages(message.Images)
	if err != nil {
		return err
	}
	_, err = s.Runs.AppendMessage(ctx, &model.SessionMessageM{
		SessionID: key,
		Role:      message.Role,
		Content:   message.Content,
		Images:    images,
		CreatedAt: time.Now().UnixMilli(),
	})
	return err
}

// Save implements SessionStore.
func (s *PGStore) Save(ctx context.Context, key string, messages []agent.Message) error {
	rows := make([]*model.SessionMessageM, 0, len(messages))
	for _, message := range messages {
		images, err := encodeImages(message.Images)
		if err != nil {
			return err
		}
		rows = append(rows, &model.SessionMessageM{
			Role:      message.Role,
			Content:   message.Content,
			Images:    images,
			CreatedAt: time.Now().UnixMilli(),
		})
	}
	return s.Runs.ReplaceMessages(ctx, key, rows)
}

// Clear implements SessionStore.
func (s *PGStore) Clear(ctx context.Context, key string) error {
	return s.Runs.ClearMessages(ctx, key)
}

// TrimOldest implements SessionStore.
func (s *PGStore) TrimOldest(ctx context.Context, key string, n int) (int, error) {
	return s.Runs.TrimOldestMessages(ctx, key, n)
}

// Query implements SessionStore.
func (s *PGStore) Query(ctx context.Context, key, substr string) ([]agent.Message, error) {
	rows, err := s.Runs.QueryMessages(ctx, key, substr)
	if err != nil {
		return nil, err
	}
	out := make([]agent.Message, 0, len(rows))
	for _, row := range rows {
		images, err := decodeImages(row.Images)
		if err != nil {
			return nil, err
		}
		out = append(out, agent.Message{Role: row.Role, Content: row.Content, Images: images})
	}
	return out, nil
}

// SetLastActive implements SessionStore.
func (s *PGStore) SetLastActive(ctx context.Context, key string, at time.Time) error {
	return s.Runs.SetLastActive(ctx, key, at.UnixMilli())
}

// GetLastActive implements SessionStore.
func (s *PGStore) GetLastActive(ctx context.Context, key string) (time.Time, error) {
	millis, err := s.Runs.GetLastActive(ctx, key)
	if err != nil || millis == 0 {
		return time.Time{}, err
	}
	return time.UnixMilli(millis), nil
}

func encodeImages(images []media.Ref) (string, error) {
	if len(images) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(images)
	if err != nil {
		return "", fmt.Errorf("encode session images: %w", err)
	}
	return string(encoded), nil
}

func decodeImages(raw string) ([]media.Ref, error) {
	if raw == "" {
		return nil, nil
	}
	var images []media.Ref
	if err := json.Unmarshal([]byte(raw), &images); err != nil {
		return nil, fmt.Errorf("decode session images: %w", err)
	}
	return images, nil
}
