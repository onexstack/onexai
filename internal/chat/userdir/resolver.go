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

// Package userdir resolves an on-call name (an email or a login name) to the
// Lark user_id a ticket group is created with and messages are sent to.
//
// It exists because on-call schedules store free text. The backend config used
// to be the only source of the name -> user_id mapping, which meant every new
// person had to be added by hand before they could be paged. The directory
// keeps that config as an explicit override and otherwise reads the account's
// Feishu binding, so selecting a user in the console is enough.
package userdir

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/onexstack/onexai/internal/chat/model"
)

// Store is the narrow lookup the directory needs. The chat store implements it.
type Store interface {
	ResolveByName(ctx context.Context, name string) (*model.UserM, *model.UserLarkBindingM, error)
}

// Resolver maps an on-call name to a Lark user_id, returning "" when the name
// cannot be resolved.
type Resolver interface {
	LarkUserID(ctx context.Context, name string) string
}

// Static is the explicit override from backend.regen-user-map. Keys are
// on-call names and values are Lark user_ids. Lookup is exact first, then
// case-insensitive, so a deployment that spelled a name differently keeps
// working.
type Static map[string]string

var _ Resolver = Static(nil)

// LarkUserID implements Resolver.
func (m Static) LarkUserID(_ context.Context, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if id := strings.TrimSpace(m[name]); id != "" {
		return id
	}
	for key, id := range m {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// Cache bounds. A positive answer is stable for longer than a negative one:
// a newly bound account starts working within negativeTTL, while a re-binding
// flips within positiveTTL.
const (
	defaultPositiveTTL = 5 * time.Minute
	defaultNegativeTTL = time.Minute
	maxCacheEntries    = 4096
)

type entry struct {
	userID  string
	expires time.Time
}

// Directory implements Resolver over the config override and the account store.
type Directory struct {
	store     Store
	overrides Static
	positive  time.Duration
	negative  time.Duration
	now       func() time.Time

	mu    sync.RWMutex
	cache map[string]entry
}

var _ Resolver = (*Directory)(nil)

// NewDirectory builds a resolver. A nil store disables the database half; the
// overrides still apply.
func NewDirectory(st Store, overrides Static, positive, negative time.Duration) *Directory {
	if positive <= 0 {
		positive = defaultPositiveTTL
	}
	if negative <= 0 {
		negative = defaultNegativeTTL
	}
	return &Directory{
		store:     st,
		overrides: overrides,
		positive:  positive,
		negative:  negative,
		now:       time.Now,
		cache:     make(map[string]entry),
	}
}

// LarkUserID implements Resolver.
func (d *Directory) LarkUserID(ctx context.Context, name string) string {
	if d == nil {
		return ""
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// An explicit override always wins, including over a database binding.
	if id := d.overrides.LarkUserID(ctx, name); id != "" {
		return id
	}

	key := strings.ToLower(name)
	if id, ok := d.cached(key); ok {
		return id
	}

	id, ttl := d.lookup(ctx, name)
	d.put(key, id, ttl)
	return id
}

func (d *Directory) lookup(ctx context.Context, name string) (string, time.Duration) {
	if d.store == nil {
		return "", d.negative
	}
	user, binding, err := d.store.ResolveByName(ctx, name)
	if err != nil {
		// A database failure says nothing about the name; retry next call.
		slog.WarnContext(ctx, "userdir: resolve account failed", "name", name, "error", err)
		return "", 0
	}
	if user != nil {
		// The override may be keyed by the account's email even when the
		// schedule stored its login name (or the other way round).
		if id := d.overrides.LarkUserID(ctx, user.Email); id != "" {
			return id, d.positive
		}
		if id := d.overrides.LarkUserID(ctx, user.Username); id != "" {
			return id, d.positive
		}
	}
	if binding != nil {
		if id := strings.TrimSpace(binding.LarkUserID); id != "" {
			return id, d.positive
		}
	}
	return "", d.negative
}

func (d *Directory) cached(key string) (string, bool) {
	d.mu.RLock()
	e, ok := d.cache[key]
	d.mu.RUnlock()
	if !ok || d.now().After(e.expires) {
		return "", false
	}
	// A cached negative answer is still a cache hit.
	return e.userID, true
}

func (d *Directory) put(key, id string, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.cache) >= maxCacheEntries {
		now := d.now()
		for k, e := range d.cache {
			if now.After(e.expires) {
				delete(d.cache, k)
			}
		}
	}
	d.cache[key] = entry{userID: id, expires: d.now().Add(ttl)}
}
