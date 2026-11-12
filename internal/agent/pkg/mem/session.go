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

// Package mem provides conversation-session storage for the AI agent flow.
// A SessionStore keeps the ordered message history for a session key. Two
// implementations are available: in-memory (single process) and Redis
// (shared across pods), selected via NewSessionStore.
package mem

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	agent "github.com/onexstack/onexai/pkg/agentapi"
)

// SessionStore persists per-session conversation history.
type SessionStore interface {
	// Load returns the message history for key (nil slice when absent).
	Load(ctx context.Context, key string) ([]agent.Message, error)
	// Append adds one message to the session history.
	Append(ctx context.Context, key string, m agent.Message) error
	// Save replaces the entire session history.
	Save(ctx context.Context, key string, msgs []agent.Message) error
	// Clear removes all history for the session.
	Clear(ctx context.Context, key string) error
	// TrimOldest drops the oldest n messages and reports how many are left.
	//
	// The count is returned rather than left to a follow-up Load because the
	// caller that trims is usually deciding whether to trim again, and a
	// separate read is a second round trip that can disagree with the first.
	TrimOldest(ctx context.Context, key string, n int) (remaining int, err error)
	// Query returns the messages whose text contains substr, case-insensitively.
	// It is a scan, not an index: it exists so a caller can find a turn it half
	// remembers, over a history that is bounded by its own budget.
	Query(ctx context.Context, key, substr string) ([]agent.Message, error)
	// SetLastActive records when the session was last used.
	SetLastActive(ctx context.Context, key string, at time.Time) error
	// GetLastActive returns when the session was last used, or the zero time
	// when it never was (or its record has expired).
	GetLastActive(ctx context.Context, key string) (time.Time, error)
}

// NewSessionStore returns a Redis-backed store when rdb is non-nil, else an
// in-memory store. ttl bounds Redis session lifetime (ignored for in-memory).
func NewSessionStore(rdb *redis.Client, keyPrefix string, ttl time.Duration) SessionStore {
	if rdb != nil {
		return &redisSessionStore{rdb: rdb, prefix: keyPrefix, ttl: ttl}
	}
	return newMemSessionStore()
}

// ---- in-memory ----

type memSessionStore struct {
	mu sync.RWMutex
	m  map[string][]agent.Message
	// lastActive is kept beside the history rather than derived from it: a
	// session whose history was trimmed to nothing is still a session that was
	// active, and deriving it would call that session idle.
	lastActive map[string]time.Time
}

func newMemSessionStore() *memSessionStore {
	return &memSessionStore{
		m:          make(map[string][]agent.Message),
		lastActive: make(map[string]time.Time),
	}
}

func (s *memSessionStore) Load(_ context.Context, key string) ([]agent.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.m[key]
	if len(src) == 0 {
		return nil, nil
	}
	out := make([]agent.Message, len(src))
	copy(out, src)
	return out, nil
}

func (s *memSessionStore) Append(_ context.Context, key string, m agent.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = append(s.m[key], m)
	return nil
}

func (s *memSessionStore) Save(_ context.Context, key string, msgs []agent.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]agent.Message, len(msgs))
	copy(cp, msgs)
	s.m[key] = cp
	return nil
}

func (s *memSessionStore) Clear(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

func (s *memSessionStore) TrimOldest(_ context.Context, key string, n int) (int, error) {
	if n <= 0 {
		return s.count(key), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	if n >= len(cur) {
		delete(s.m, key)
		return 0, nil
	}
	s.m[key] = append([]agent.Message(nil), cur[n:]...)
	return len(s.m[key]), nil
}

// count returns the current history length.
func (s *memSessionStore) count(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.m[key])
}

func (s *memSessionStore) Query(_ context.Context, key, substr string) ([]agent.Message, error) {
	substr = strings.ToLower(strings.TrimSpace(substr))
	if substr == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []agent.Message
	for _, m := range s.m[key] {
		if strings.Contains(strings.ToLower(m.Content), substr) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *memSessionStore) SetLastActive(_ context.Context, key string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActive[key] = at
	return nil
}

func (s *memSessionStore) GetLastActive(_ context.Context, key string) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastActive[key], nil
}

// ---- redis ----

type redisSessionStore struct {
	rdb    *redis.Client
	prefix string
	ttl    time.Duration
}

func (s *redisSessionStore) key(k string) string {
	return s.prefix + ":session:" + k
}

func (s *redisSessionStore) Load(ctx context.Context, key string) ([]agent.Message, error) {
	raws, err := s.rdb.LRange(ctx, s.key(key), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	if len(raws) == 0 {
		return nil, nil
	}
	msgs := make([]agent.Message, 0, len(raws))
	for _, raw := range raws {
		var m agent.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

func (s *redisSessionStore) Save(ctx context.Context, key string, msgs []agent.Message) error {
	k := s.key(key)
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, k)
	for _, m := range msgs {
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		pipe.RPush(ctx, k, raw)
	}
	pipe.Expire(ctx, k, s.ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *redisSessionStore) Append(ctx context.Context, key string, m agent.Message) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	k := s.key(key)
	pipe := s.rdb.TxPipeline()
	pipe.RPush(ctx, k, raw)
	pipe.Expire(ctx, k, s.ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *redisSessionStore) Clear(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, s.key(key)).Err()
}

func (s *redisSessionStore) TrimOldest(ctx context.Context, key string, n int) (int, error) {
	if n <= 0 {
		return int(s.rdb.LLen(ctx, s.key(key)).Val()), nil
	}
	k := s.key(key)
	pipe := s.rdb.TxPipeline()
	pipe.LTrim(ctx, k, int64(n), -1)
	pipe.Expire(ctx, k, s.ttl)
	length := pipe.LLen(ctx, k)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return int(length.Val()), nil
}

// queryLimit bounds how much history a search reads. A session's history is
// already bounded by the prompt budget, so this is a ceiling rather than a
// working limit; it exists so a pathological list cannot be read whole.
const queryLimit = 1000

func (s *redisSessionStore) Query(ctx context.Context, key, substr string) ([]agent.Message, error) {
	substr = strings.ToLower(strings.TrimSpace(substr))
	if substr == "" {
		return nil, nil
	}
	msgs, err := s.Load(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(msgs) > queryLimit {
		msgs = msgs[len(msgs)-queryLimit:]
	}
	var out []agent.Message
	for _, m := range msgs {
		if strings.Contains(strings.ToLower(m.Content), substr) {
			out = append(out, m)
		}
	}
	return out, nil
}

// lastActiveKey is the side key holding the session's last-use time. It is
// separate from the history list because the two have different lifetimes: the
// history is trimmed and expires with the session, while "when was this last
// used" must survive a trim to be worth anything.
func (s *redisSessionStore) lastActiveKey(k string) string {
	return s.key(k) + ":lastactive"
}

func (s *redisSessionStore) SetLastActive(ctx context.Context, key string, at time.Time) error {
	k := s.lastActiveKey(key)
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, k, at.UnixMilli(), s.ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *redisSessionStore) GetLastActive(ctx context.Context, key string) (time.Time, error) {
	raw, err := s.rdb.Get(ctx, s.lastActiveKey(key)).Int64()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return time.UnixMilli(raw), nil
}
