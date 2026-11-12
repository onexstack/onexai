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

// Package checkpoint stores suspended agent runs so that answering one is a
// separate request that may be served by any pod.
//
// It implements eino's adk.CheckPointStore, which the runner uses to persist a
// run the moment it interrupts. eino only ever reads and writes a checkpoint, so
// the rest of a checkpoint's lifecycle is this package's business: removing one
// once it has been answered, and keeping the run's own metadata (agent
// selection, session, the suspension awaiting an answer) beside it under the
// same key and the same lifetime.
package checkpoint

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	agent "github.com/onexstack/onexai/pkg/agentapi"
)

// Store is a suspended-run store. Get and Set are the two methods eino needs;
// the rest is the lifecycle eino leaves to the caller.
type Store interface {
	// Get returns the checkpoint for id, and whether it exists.
	Get(ctx context.Context, id string) ([]byte, bool, error)
	// Set writes the checkpoint for id.
	Set(ctx context.Context, id string, data []byte) error
	// Delete removes id's checkpoint and metadata. Deleting something that is
	// not there is not an error: the second click on an approval card is
	// expected, and it is the caller that reports it as already handled.
	Delete(ctx context.Context, id string) error
	// PutMeta stores the metadata a resume needs to rebuild the run.
	PutMeta(ctx context.Context, id string, meta agent.RunMeta) error
	// GetMeta returns the metadata for id, and whether it exists.
	GetMeta(ctx context.Context, id string) (agent.RunMeta, bool, error)
	// Claim atomically records that requestID has been answered, and reports
	// whether this call is the one that answered it.
	//
	// Answering is not idempotent — it resumes the run and executes the tools it
	// paused on — so a double click must not resume twice. Claim is what makes
	// "first one wins" atomic across pods: two clicks that both read the metadata
	// would both see an unanswered suspension and both resume it, and for a gate
	// around a destructive tool that is exactly the harm the gate exists to
	// prevent. A later click is expected rather than exceptional, so a refused
	// claim is a normal false, not an error.
	Claim(ctx context.Context, id, requestID string) (bool, error)
}

// New returns a Redis-backed store when rdb is non-nil, else an in-memory one.
//
// ttl bounds a checkpoint's life in Redis, and is the only garbage collection a
// suspended run gets: it is what stops an approval nobody ever answers from
// occupying a key forever. The in-memory store ignores it, exactly as the
// session store does — it exists for the single-process deployment, where a
// suspension lives until it is answered or the process restarts. Nothing
// depends on the TTL for correctness: a stale answer is refused because its
// RequestID no longer matches, which holds with or without an expiry.
func New(rdb *redis.Client, keyPrefix string, ttl time.Duration) Store {
	if rdb != nil {
		return &redisStore{rdb: rdb, prefix: keyPrefix, ttl: ttl}
	}
	return newMemStore()
}

// ---- in-memory ----

type memStore struct {
	mu     sync.RWMutex
	data   map[string][]byte
	metas  map[string]agent.RunMeta
	claims map[string]map[string]bool // run id -> request id -> answered
}

func newMemStore() *memStore {
	return &memStore{
		data:   make(map[string][]byte),
		metas:  make(map[string]agent.RunMeta),
		claims: make(map[string]map[string]bool),
	}
}

func (s *memStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.data[id]
	return b, ok, nil
}

func (s *memStore) Set(_ context.Context, id string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[id] = data
	return nil
}

func (s *memStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
	delete(s.metas, id)
	// Claims go with the run here, while Redis leaves them to expire. The
	// difference is not observable: what a claim guards against is a second
	// answer, and with the checkpoint gone the resume that answer triggers has
	// nothing to continue and is refused on its own. Dropping them is what keeps
	// the map from growing by one entry per answered approval for the life of
	// the process.
	delete(s.claims, id)
	return nil
}

func (s *memStore) PutMeta(_ context.Context, id string, meta agent.RunMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metas[id] = meta
	return nil
}

func (s *memStore) GetMeta(_ context.Context, id string) (agent.RunMeta, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.metas[id]
	return m, ok, nil
}

func (s *memStore) Claim(_ context.Context, id, requestID string) (bool, error) {
	if requestID == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claims[id][requestID] {
		return false, nil
	}
	if s.claims[id] == nil {
		s.claims[id] = make(map[string]bool)
	}
	s.claims[id][requestID] = true
	return true, nil
}

// ---- redis ----

type redisStore struct {
	rdb    *redis.Client
	prefix string
	ttl    time.Duration
}

func (s *redisStore) checkpointKey(id string) string { return s.prefix + ":agent:cp:" + id }
func (s *redisStore) metaKey(id string) string       { return s.prefix + ":agent:cpmeta:" + id }

// claimKey is keyed by request id as well as run id, so answering a later
// suspension of the same run is not blocked by an earlier answer. Claims are
// left to expire rather than deleted with the checkpoint: Delete would have to
// enumerate the request ids of a run that is already gone, and a request id is
// a fresh UUID per suspension, so nothing can legitimately present an old one.
func (s *redisStore) claimKey(id, requestID string) string {
	return s.prefix + ":agent:cpclaim:" + id + ":" + requestID
}

func (s *redisStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	b, err := s.rdb.Get(ctx, s.checkpointKey(id)).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (s *redisStore) Set(ctx context.Context, id string, data []byte) error {
	return s.rdb.Set(ctx, s.checkpointKey(id), data, s.ttl).Err()
}

func (s *redisStore) Delete(ctx context.Context, id string) error {
	// The checkpoint goes first. A resume reads the metadata to find the
	// addresses it must target and the checkpoint to execute them against, so
	// leaving the metadata briefly behind cannot produce a half-run; leaving the
	// checkpoint behind after the metadata is gone is the state that would.
	return s.rdb.Del(ctx, s.checkpointKey(id), s.metaKey(id)).Err()
}

func (s *redisStore) PutMeta(ctx context.Context, id string, meta agent.RunMeta) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, s.metaKey(id), raw, s.ttl).Err()
}

func (s *redisStore) GetMeta(ctx context.Context, id string) (agent.RunMeta, bool, error) {
	var meta agent.RunMeta
	raw, err := s.rdb.Get(ctx, s.metaKey(id)).Bytes()
	if err == redis.Nil {
		return meta, false, nil
	}
	if err != nil {
		return meta, false, err
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return meta, false, err
	}
	return meta, true, nil
}

func (s *redisStore) Claim(ctx context.Context, id, requestID string) (bool, error) {
	if requestID == "" {
		return false, nil
	}
	return s.rdb.SetNX(ctx, s.claimKey(id, requestID), "1", s.ttl).Result()
}
