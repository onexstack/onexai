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

package agentcmd

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// ttlEntry is one key's cached value and the moment it was read.
type ttlEntry[V any] struct {
	at    time.Time
	value V
}

// ttlCache is the per-key, serve-stale-while-refreshing cache the agent command
// resolver uses for its two remote sources.
//
// The semantics are load-bearing and shared by both sources: a miss waits for
// the read (a cold menu must not grant nothing), a stale hit serves what is
// known and refreshes off the request path, and a failed read still records the
// previous value with a fresh timestamp so one broken dependency does not turn
// into one upstream call per message.
type ttlCache[K ~string, V any] struct {
	ttl     time.Duration
	now     func() time.Time
	loader  func(context.Context, K) (V, error)
	cold    func(context.Context, K, error)
	refresh func(context.Context, K, error)
	op      string

	mu      sync.Mutex
	entries map[K]ttlEntry[V]
	sf      singleflight.Group
}

type ttlCacheOptions[K ~string, V any] struct {
	TTL     time.Duration
	Now     func() time.Time
	Load    func(context.Context, K) (V, error)
	Cold    func(context.Context, K, error)
	Refresh func(context.Context, K, error)
	Op      string
}

func newTTLCache[K ~string, V any](opts ttlCacheOptions[K, V]) *ttlCache[K, V] {
	return &ttlCache[K, V]{
		ttl:     opts.TTL,
		now:     opts.Now,
		loader:  opts.Load,
		cold:    opts.Cold,
		refresh: opts.Refresh,
		op:      opts.Op,
		entries: make(map[K]ttlEntry[V]),
	}
}

// get returns the key's value, loading it synchronously on a miss and
// refreshing it in the background when it has gone stale.
func (c *ttlCache[K, V]) get(ctx context.Context, key K, now time.Time) V {
	entry, cached := c.cached(key)
	if cached && now.Sub(entry.at) < c.ttl {
		return entry.value
	}

	if !cached {
		value, err := c.load(ctx, key, now)
		if err != nil {
			if c.cold != nil {
				c.cold(ctx, key, err)
			}
			var zero V
			return zero
		}
		return value
	}

	bg := context.WithoutCancel(ctx)
	runtime.GoSafe(bg, c.op, func() {
		if _, err := c.load(bg, key, c.now()); err != nil && c.refresh != nil {
			c.refresh(bg, key, err)
		}
	})
	return entry.value
}

// load reads one key, collapsing concurrent reads for the same key. A failed
// read still records the value it last knew, stamped with now.
func (c *ttlCache[K, V]) load(ctx context.Context, key K, now time.Time) (V, error) {
	v, err, _ := c.sf.Do(string(key), func() (any, error) {
		if entry, ok := c.cached(key); ok && now.Sub(entry.at) < c.ttl {
			return entry.value, nil
		}

		value, err := c.loader(ctx, key)
		if err != nil {
			var kept V
			if entry, ok := c.cached(key); ok {
				kept = entry.value
			}
			c.remember(key, kept, now)
			return kept, err
		}

		c.remember(key, value, now)
		return value, nil
	})
	if err != nil {
		var zero V
		return zero, err
	}
	return v.(V), nil
}

func (c *ttlCache[K, V]) cached(key K) (ttlEntry[V], bool) {
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	return entry, ok
}

func (c *ttlCache[K, V]) remember(key K, value V, at time.Time) {
	c.mu.Lock()
	c.entries[key] = ttlEntry[V]{at: at, value: value}
	c.mu.Unlock()
}
