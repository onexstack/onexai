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

package authn

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// nonceTTL is how long a nonce is remembered. It matches the timestamp skew
// window maxTimestampSkew: a request whose timestamp is older than that is
// rejected by the timestamp check before the nonce is consulted, so remembering
// a nonce for longer than the window serves no request.
const nonceTTL = maxTimestampSkew

// NonceStore records the nonces seen within the replay window.
//
// A nonce is part of the signed string, so only a caller holding the secret key
// can produce one that verifies. Recording it here is what stops that verified
// request from being replayed: the timestamp check bounds a captured request to
// the skew window, and the nonce makes the same request usable only once inside
// that window.
type NonceStore interface {
	// Claim atomically reserves nonce for apiKey. It returns true when the nonce
	// had not been seen, false when it is a replay within the window. A non-nil
	// error means the check could not be made; the caller must then refuse the
	// request rather than authenticate it unchecked.
	Claim(ctx context.Context, apiKey, nonce string) (bool, error)
}

// NewNonceStore returns a Redis-backed store when rdb is non-nil, and a
// process-local one otherwise.
//
// The Redis store is the deployment form: a request captured from one pod can be
// replayed against another, so only a shared store rejects it everywhere. The
// in-memory fallback is for a single-process deployment (redis.addr empty); it
// gives no cross-pod protection, which is a real limitation and the reason the
// options validation treats Redis as the supported configuration for anything
// replicated.
func NewNonceStore(rdb *redis.Client, keyPrefix string) NonceStore {
	if rdb == nil {
		return newMemoryNonceStore()
	}
	if keyPrefix == "" {
		keyPrefix = "onexai"
	}
	return &redisNonceStore{rdb: rdb, prefix: keyPrefix}
}

type redisNonceStore struct {
	rdb    *redis.Client
	prefix string
}

var _ NonceStore = (*redisNonceStore)(nil)

func (s *redisNonceStore) Claim(ctx context.Context, apiKey, nonce string) (bool, error) {
	// SetNX is the atomic set-if-absent: exactly one concurrent caller gets true.
	return s.rdb.SetNX(ctx, s.prefix+":agent:nonce:"+apiKey+":"+nonce, "1", nonceTTL).Result()
}

// memoryNonceStore is the single-process fallback. It is correct for one server
// and wrong for a fleet: a replay routed to a different pod finds a store that
// never saw the first request.
type memoryNonceStore struct {
	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time
}

var _ NonceStore = (*memoryNonceStore)(nil)

func newMemoryNonceStore() *memoryNonceStore {
	return &memoryNonceStore{seen: make(map[string]time.Time), now: time.Now}
}

func (s *memoryNonceStore) Claim(_ context.Context, apiKey, nonce string) (bool, error) {
	key := apiKey + "\x00" + nonce
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Sweep expired entries so the map cannot grow by one entry per request for
	// the life of the process. The sweep is bounded by the replay window, which
	// is small.
	for k, expiresAt := range s.seen {
		if !now.Before(expiresAt) {
			delete(s.seen, k)
		}
	}

	if expiresAt, ok := s.seen[key]; ok && now.Before(expiresAt) {
		return false, nil
	}
	s.seen[key] = now.Add(nonceTTL)
	return true, nil
}
