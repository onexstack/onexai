// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// Package idempotent records the idempotency key of a mutating request so the
// same request cannot be executed twice.
//
// A retry is not a rare case: a client that loses the response to a request
// that succeeded has no way to tell success from failure, and its only correct
// action is to send it again. For a write that ran a deploy or started an agent
// run, that second execution is a real second side effect.
//
// The key is claimed before the handler runs, so two concurrent copies cannot
// both proceed. A claim that fails to produce a usable result is released, so a
// server error does not lock the key out for the life of its TTL.
package idempotent

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultTTL is how long a key is remembered. It has to outlast a client's
// retry budget, and be short enough that a reused key is not refused a day
// later by a claim whose response nobody is waiting for any more.
const DefaultTTL = 24 * time.Hour

// Store records idempotency keys.
type Store interface {
	// Claim atomically reserves key. It returns fresh=true for the first caller
	// and fresh=false for every later one, for as long as the TTL lasts. A
	// non-nil error means the store could not answer, and the caller decides:
	// the middleware refuses the request rather than risk a double execution.
	Claim(ctx context.Context, key string) (fresh bool, err error)
	// Release drops a claim so a request that failed to do anything can be
	// retried immediately.
	Release(ctx context.Context, key string) error
}

// New returns a Redis-backed store when rdb is non-nil, and a process-local one
// otherwise.
//
// The Redis store is the deployment form: a retry routed to a different pod
// finds the claim the first pod recorded. The in-memory fallback protects a
// single process only, which is the same trade-off the AK/SK nonce store makes
// and is only correct while the deployment has one replica.
func New(rdb *redis.Client, keyPrefix string) Store {
	if rdb == nil {
		return newMemoryStore(DefaultTTL)
	}

	if keyPrefix == "" {
		keyPrefix = "onexai"
	}

	return &redisStore{rdb: rdb, prefix: keyPrefix, ttl: DefaultTTL}
}

type redisStore struct {
	rdb    *redis.Client
	prefix string
	ttl    time.Duration
}

var _ Store = (*redisStore)(nil)

func (s *redisStore) Claim(ctx context.Context, key string) (bool, error) {
	// SetNX is the atomic set-if-absent: exactly one concurrent caller wins.
	return s.rdb.SetNX(ctx, s.prefix+":idempotency:"+key, "1", s.ttl).Result()
}

func (s *redisStore) Release(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, s.prefix+":idempotency:"+key).Err()
}

// memoryStore is the single-process fallback.
type memoryStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	claimed map[string]time.Time
	now     func() time.Time
}

var _ Store = (*memoryStore)(nil)

func newMemoryStore(ttl time.Duration) *memoryStore {
	return &memoryStore{ttl: ttl, claimed: make(map[string]time.Time), now: time.Now}
}

func (s *memoryStore) Claim(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if expired, ok := s.claimed[key]; ok && now.Sub(expired) < s.ttl {
		return false, nil
	}

	// Expired entries are overwritten rather than swept: the map only holds one
	// entry per key in flight, so its size is bounded by traffic, not by time.
	s.claimed[key] = now

	return true, nil
}

func (s *memoryStore) Release(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.claimed, key)

	return nil
}
