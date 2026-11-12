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

package declstore

import (
	"context"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// DefaultGetCacheSize is the default bound of a Get cache.
const DefaultGetCacheSize = 10000

// GetCache bounds the memory a reader spends on Get when the process holds no
// rows for the kind.
//
// Without an informer every Get is an API round trip, and a runtime resolves a
// capability set by name — twenty skills is twenty round trips per run. The cache
// makes that affordable, and being bounded is what makes the kind's memory stop
// tracking the object count.
//
// Eviction is arbitrary rather than least-recently-used: a strict LRU needs an
// ordered structure and a lock held across a list splice, and the win over
// "drop any entry" is not worth that for entries that are cheap to re-fetch.
type GetCache[M any] struct {
	mu    sync.Mutex
	limit int
	items map[string]M
}

// NewGetCache builds a cache bounded to limit entries; <= 0 takes the default.
func NewGetCache[M any](limit int) *GetCache[M] {
	if limit <= 0 {
		limit = DefaultGetCacheSize
	}
	return &GetCache[M]{limit: limit, items: make(map[string]M, limit)}
}

// Get returns the cached row, or reads it through load and caches the result. A
// key load reports absent is not cached: absence is cheap to re-check and
// caching it would need invalidating on create.
func (c *GetCache[M]) Get(
	ctx context.Context, key string, load func(context.Context) (M, bool, error),
) (M, bool, error) {
	c.mu.Lock()
	if row, ok := c.items[key]; ok {
		c.mu.Unlock()
		return row, true, nil
	}
	c.mu.Unlock()

	row, found, err := load(ctx)
	if err != nil || !found {
		return row, found, err
	}

	c.mu.Lock()
	if len(c.items) >= c.limit {
		for victim := range c.items {
			delete(c.items, victim)
			break
		}
	}
	c.items[key] = row
	c.mu.Unlock()
	return row, true, nil
}

// Forget drops one entry.
func (c *GetCache[M]) Forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, key)
}

// APIGetByKey answers a Get from the API server behind the cache, turning a
// NotFound into "absent" rather than an error.
func APIGetByKey[M any](
	cache *GetCache[M], get func(context.Context, string) (M, error),
) func(context.Context, string) (M, bool, error) {
	return func(ctx context.Context, name string) (M, bool, error) {
		return cache.Get(ctx, name, func(ctx context.Context) (M, bool, error) {
			var zero M
			row, err := get(ctx, name)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return zero, false, nil
				}
				return zero, false, err
			}
			return row, true, nil
		})
	}
}
