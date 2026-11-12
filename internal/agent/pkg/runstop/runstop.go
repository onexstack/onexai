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

// Package runstop provides a registry for cancelling in-flight AI runs by a
// run ID. The local implementation cancels runs in the current process; the
// Redis implementation additionally broadcasts stop requests over Pub/Sub so a
// stop clicked on one pod cancels a run executing on another.
package runstop

import (
	"context"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

// Registry registers cancellable runs and services stop requests.
type Registry interface {
	// Register associates runID with a cancel func and returns a deregister
	// func to call when the run finishes.
	Register(runID string, cancel func()) (deregister func())
	// RequestStop cancels the run identified by runID (locally, and across pods
	// for the Redis implementation).
	RequestStop(ctx context.Context, runID string) error
	// StopLocal cancels a run if it is registered in this process, reporting
	// whether it was found.
	StopLocal(runID string) bool
}

// local is the base in-process registry.
type local struct {
	mu      sync.Mutex
	cancels map[string]func()
}

// NewLocal returns an in-process stop registry.
func NewLocal() *local {
	return &local{cancels: make(map[string]func())}
}

func (r *local) Register(runID string, cancel func()) func() {
	r.mu.Lock()
	r.cancels[runID] = cancel
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.cancels, runID)
		r.mu.Unlock()
	}
}

func (r *local) StopLocal(runID string) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[runID]
	r.mu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
	return ok
}

func (r *local) RequestStop(_ context.Context, runID string) error {
	r.StopLocal(runID)
	return nil
}

// redisRegistry adds cross-pod stop broadcasting over Redis Pub/Sub.
type redisRegistry struct {
	*local
	rdb     *redis.Client
	channel string
}

// New returns a Registry. When rdb is non-nil it broadcasts stop requests over
// the Pub/Sub channel "<keyPrefix>:askai:stop"; otherwise it is process-local.
// For the Redis variant, call Subscribe(ctx) once at startup.
func New(rdb *redis.Client, keyPrefix string) Registry {
	if rdb == nil {
		return NewLocal()
	}
	return &redisRegistry{
		local:   NewLocal(),
		rdb:     rdb,
		channel: keyPrefix + ":askai:stop",
	}
}

func (r *redisRegistry) RequestStop(ctx context.Context, runID string) error {
	// Stop locally first (fast path when the run is on this pod).
	r.StopLocal(runID)
	// Broadcast so other pods can stop their local run.
	return r.rdb.Publish(ctx, r.channel, runID).Err()
}

// Subscribe listens for cross-pod stop requests until ctx is cancelled. It
// should be launched in a goroutine at startup.
func (r *redisRegistry) Subscribe(ctx context.Context) {
	sub := r.rdb.Subscribe(ctx, r.channel)
	defer func() { _ = sub.Close() }()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-ch:
			if !ok {
				return
			}
			if stopped := r.StopLocal(message.Payload); stopped {
				slog.InfoContext(ctx, "stopped AI run via cross-pod signal", "run_id", message.Payload)
			}
		}
	}
}

// Subscriber is implemented by registries that need a background subscription
// loop (the Redis variant). Callers type-assert to start it.
type Subscriber interface {
	Subscribe(ctx context.Context)
}
