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

// Package roleset holds the deployment's role lists — who may run SRE-only and
// admin-only commands — as they are read from chat.setting.
//
// # Why this is not a config lookup
//
// The two lists used to come from YAML (lark.sre-user-ids,
// chat.roles.admin-user-ids), which made every change a config edit plus a
// restart, and made a missing key indistinguishable from an intentional one: an
// empty list is a legitimate value that grants nobody anything. They now live in
// the database and are re-read on a timer, so a change is an UPDATE that takes
// effect without a restart.
//
// # Why a cache at all
//
// Every inbound message asks twice (SRE and admin) while the lists change on the
// order of once a month. The lookups are therefore a map read over an immutable
// snapshot; the database is touched once per refresh, never on the message path.
//
// The snapshot is replaced whole (see onexstack/pkg/astore), so a reader sees one
// generation, never a half-updated one. A refresh that fails keeps the previous
// generation — a transient database error must not revoke the whole deployment's
// authority.
package roleset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/astore"
)

// The setting keys the role lists live under. They are constants rather than
// literals so the key an operator writes and the key this package reads cannot
// drift apart.
const (
	// KeyAdminUserIDs holds the admins: the users who may run the admin-only
	// commands (/new, /switch, /sessions, /doctor, /dir, /agent).
	KeyAdminUserIDs = "onexai-admin-user-id"
	// KeySREUserIDs holds the SREs: the users who may run the SRE-only
	// commands (/close, /hand, /assign, /pipeline).
	KeySREUserIDs = "onexai-sre-user-id"
)

// Errors a refresh can report.
var (
	// ErrNoSource is returned by Refresh or Start on a cache built without a
	// source. It is an error rather than a silent no-op because the caller that
	// gets it is the one that decides whether a deployment starting with no role
	// lists is acceptable.
	ErrNoSource = errors.New("roleset: no settings source")
	// errNilCache reports a Refresh on a cache that was never built. It is a
	// programming error rather than a deployment one, which is why it is not
	// exported: no caller is expected to act on it beyond logging it.
	errNilCache = errors.New("roleset: refresh on a nil cache")
)

// Row is one key/value pair the role lists are read from.
//
// It is this package's own type rather than a store model so a source can be a
// database table, a set of RBAC bindings, or a test literal without any of them
// having to know about the others.
type Row struct {
	Key   string
	Value string
}

// Source loads the rows. It is deliberately narrower than any store: this
// package never writes.
type Source interface {
	ListAll(ctx context.Context) ([]*Row, error)
}

// Status describes one generation of the role lists.
//
// It reports sizes rather than the identities themselves: it exists for the
// /doctor report, and a list of operators in a chat log outlives the question
// that produced it.
type Status struct {
	// SRE and Admin are the sizes of the two lists. Admin includes every SRE.
	SRE, Admin int
	// LoadedAt is when this generation was read; zero means nothing has been
	// read successfully yet, which is the state in which nobody has a role.
	LoadedAt time.Time
	// Err is the last refresh failure, or nil if the last refresh succeeded.
	Err error
}

// snapshot is one generation of the two sets. The maps are never mutated after
// being published.
type snapshot struct {
	sre   map[string]struct{}
	admin map[string]struct{}
}

// Cache is the live pair of role lists.
//
// Every method is safe on a nil receiver and answers "no role": a cache that was
// never built (or a runtime whose roles were not wired) must deny rather than
// grant.
type Cache struct {
	source Source
	cache  *astore.Store[snapshot]
	// live makes every read answer from the source instead of the snapshot.
	// It is what a source that is already an in-memory cache (an informer)
	// wants: polling it would add staleness without saving a thing.
	live bool
}

// New returns a cache that reads its lists from source and, once Start is
// called, re-reads them a minute at a time. It performs no I/O here: the first
// read is an explicit Refresh or Start, which the caller runs on a schedule it
// controls and can report the failure of. Until then every lookup answers false.
func New(source Source) *Cache {
	return NewWithInterval(source, 0)
}

// NewWithInterval is New with the refresh interval made explicit. A non-positive
// interval means no timer: the caller drives Refresh itself.
func NewWithInterval(source Source, interval time.Duration) *Cache {
	c := &Cache{source: source}
	c.cache = astore.New(astore.Options{
		Name:     "roleset",
		Interval: interval,
	}, c.load)
	return c
}

// NewLive returns a cache that answers every read from the source.
//
// It is for a source backed by an informer: the lister is already the cache, so
// a snapshot on top of it could only get older. A source read that fails falls
// back to the last snapshot, which is the same fail-safe the timed path has.
func NewLive(source Source) *Cache {
	c := New(source)
	c.live = true
	return c
}

// NewStatic returns a cache holding fixed sets. It exists for tests and for any
// construction that has no database behind it; the live path uses New.
//
// The SRE list is added to the admin list here for the same reason load does
// it: an SRE is an administrator. Both inputs are copied, so the caller keeps
// its own maps.
func NewStatic(sre, admin map[string]struct{}) *Cache {
	c := New(nil)
	c.cache.Seed(union(sre, admin))
	return c
}

// union copies the two lists into one generation, with every SRE an admin.
func union(sre, admin map[string]struct{}) snapshot {
	nextSRE := make(map[string]struct{}, len(sre))
	for id := range sre {
		nextSRE[id] = struct{}{}
	}
	nextAdmin := make(map[string]struct{}, len(admin)+len(nextSRE))
	for id := range admin {
		nextAdmin[id] = struct{}{}
	}
	for id := range nextSRE {
		nextAdmin[id] = struct{}{}
	}
	return snapshot{sre: nextSRE, admin: nextAdmin}
}

// load reads one generation and reports what changed.
//
// It runs inside the store's refresh, so it must not mutate anything it returns:
// readers may still be serving the previous generation.
func (c *Cache) load(ctx context.Context) (snapshot, error) {
	if c.source == nil {
		return snapshot{}, ErrNoSource
	}

	rows, err := c.source.ListAll(ctx)
	if err != nil {
		return snapshot{}, fmt.Errorf("roleset: list settings: %w", err)
	}

	next := generation(ctx, rows)
	prev, hadPrev := c.cache.Current()

	// The first load is always reported, even when it found nothing: an empty
	// list is the state in which nobody can run a restricted command, and it is
	// the state a deployment that has just been switched over starts in. A line
	// only printed on change would be silent for exactly that case.
	//
	// Later loads are reported only when the lists changed. This runs every
	// refresh, and a line a minute that always says the same thing is a line
	// nobody reads.
	switch {
	case !hadPrev && len(next.admin) == 0:
		slog.WarnContext(ctx,
			"roleset: no administrators are configured; every admin-only command is refused until chat.setting carries the key",
			"key", KeyAdminUserIDs, "sre", len(next.sre), "admins", len(next.admin))
	case !hadPrev || !sameSet(prev.Value.sre, next.sre) || !sameSet(prev.Value.admin, next.admin):
		slog.InfoContext(ctx, "roleset: role lists loaded", "sre", len(next.sre), "admins", len(next.admin))
	}
	return next, nil
}

// Start loads the first generation and then re-reads it on the configured
// interval until ctx is done.
//
// A failed first load is reported but does not stop the timer: the role lists
// stay empty until a retry succeeds, and a deployment that came up while the
// database was unavailable recovers on its own.
func (c *Cache) Start(ctx context.Context) error {
	if c == nil {
		return errNilCache
	}
	if c.source == nil {
		return ErrNoSource
	}
	return c.cache.Start(ctx)
}

// Refresh reloads the role lists from the source.
//
// A failure leaves the previous generation in place and records the error in
// Status: the deployment keeps the authority it had rather than losing it to a
// database blip. The error is also returned so that a caller doing the first
// load can report it.
func (c *Cache) Refresh(ctx context.Context) error {
	if c == nil {
		return errNilCache
	}
	if c.source == nil {
		return ErrNoSource
	}
	return c.cache.Refresh(ctx)
}

// generation builds the two sets one load produces from the settings rows.
//
// It is a pure function of the rows: a row that is missing, nil, or keyless
// contributes nothing, and a key that is absent yields an empty list. Both are
// the same answer — nobody holds that role.
func generation(ctx context.Context, rows []*Row) snapshot {
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		if row == nil || row.Key == "" {
			continue
		}
		values[row.Key] = row.Value
	}
	return union(
		parseIDs(ctx, KeySREUserIDs, values[KeySREUserIDs]),
		parseIDs(ctx, KeyAdminUserIDs, values[KeyAdminUserIDs]),
	)
}

// IsSREUser reports whether the given Lark user_id is on the SRE list.
func (c *Cache) IsSREUser(userID string) bool {
	if c == nil || userID == "" {
		return false
	}
	_, ok := c.snapshotForRead().sre[userID]
	return ok
}

// IsAdminUser reports whether the given Lark user_id may run admin-only
// commands. Every SRE is an administrator, so the admin list is the union of
// both rows.
func (c *Cache) IsAdminUser(userID string) bool {
	if c == nil || userID == "" {
		return false
	}
	_, ok := c.snapshotForRead().admin[userID]
	return ok
}

// snapshotForRead returns the generation a lookup should answer from: a fresh
// source read in live mode, the snapshot otherwise.
func (c *Cache) snapshotForRead() snapshot {
	if c.live {
		if current, err := c.load(context.Background()); err == nil {
			return current
		}
	}
	gen, ok := c.cache.Current()
	if !ok {
		return snapshot{}
	}
	return gen.Value
}

// Status reports the current generation's sizes and how it was obtained.
func (c *Cache) Status() Status {
	if c == nil {
		return Status{}
	}
	loadedAt, err := c.cache.Status()
	status := Status{LoadedAt: loadedAt, Err: err}
	if gen, ok := c.cache.Current(); ok {
		status.SRE = len(gen.Value.sre)
		status.Admin = len(gen.Value.admin)
	}
	return status
}

// parseIDs decodes one role list.
//
// The canonical value is a JSON array of Lark user_ids — ["user_id_a","user_id_b"]
// — which is what the release notes and the INSERTs in the docs use. A value
// that does not parse as JSON is split on commas instead, with a warning. That
// tolerance is deliberate: these rows are written by hand with SQL, and a
// missing pair of brackets would otherwise parse to "nobody is an
// administrator", which is exactly the silent lockout this package exists to
// make unnecessary.
//
// Anything that yields no ids — an empty value, "[]", a missing key — yields an
// empty set, i.e. the role is granted to nobody.
func parseIDs(ctx context.Context, key, raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]struct{}{}
	}

	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		slog.WarnContext(ctx, "roleset: setting is not a JSON array; reading it as a comma-separated list",
			"key", key, "error", err)
		list = strings.Split(raw, ",")
	}

	set := make(map[string]struct{}, len(list))
	for _, item := range list {
		if id := strings.TrimSpace(item); id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

// sameSet reports whether two id sets hold the same members.
func sameSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}
