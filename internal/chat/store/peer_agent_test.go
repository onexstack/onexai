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

package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/model"
)

// newPeerAgentStoreFor opens a peerAgentStore over a fresh database.
func newPeerAgentStoreFor(t *testing.T) (*peerAgentStore, *gorm.DB) {
	t.Helper()
	db := newStoreTestDB(t, &model.PeerAgentM{})
	return newPeerAgentStore(&store{db: db}), db
}

// binding builds a row for the given triple.
func binding(backend, scope, peerID, canonical, agents string) *model.PeerAgentM {
	return &model.PeerAgentM{
		Backend: backend, Scope: scope, PeerID: peerID,
		Canonical: canonical, Agents: agents, Source: "onex-iam",
	}
}

// TestPeerAgentPutThenGet pins the round trip and that a miss is (nil, nil)
// rather than an error — every caller has to tell "not bound" apart from "the
// lookup failed", and most peers are not bound.
func TestPeerAgentPutThenGet(t *testing.T) {
	ctx := context.Background()
	s, _ := newPeerAgentStoreFor(t)

	if err := s.Put(ctx, binding("lark", "cli_example", "ou_alice", "user-alice", `["onex-u-user-alice"]`)); err != nil {
		t.Fatalf("Put() = %v", err)
	}

	got, err := s.GetByPeer(ctx, "lark", "cli_example", "ou_alice")
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if got == nil {
		t.Fatal("Get() returned no row for one that was just written")
	}
	if got.Canonical != "user-alice" || got.Agents != `["onex-u-user-alice"]` || got.Source != "onex-iam" {
		t.Errorf("Get() = %+v, want the three written columns back", got)
	}

	// The ordinary case: someone who has never bound anything.
	miss, err := s.GetByPeer(ctx, "lark", "cli_example", "ou_nobody")
	if err != nil {
		t.Fatalf("Get() on a miss = %v, want no error", err)
	}
	if miss != nil {
		t.Errorf("Get() on a miss = %+v, want nil", miss)
	}
}

// TestPeerAgentPutIsAnUpsertOnTheTriple pins the write shape the plan calls
// "upsert, not a second row". A binding is pushed again on every membership
// change and every reconcile, so this is the steady state rather than an edge
// case — a second row would make the lookup return whichever the database
// happened to order first.
func TestPeerAgentPutIsAnUpsertOnTheTriple(t *testing.T) {
	ctx := context.Background()
	s, db := newPeerAgentStoreFor(t)

	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first := binding("lark", "cli_app", "ou_alice", "user-alice", `["onex-u-user-alice"]`)
	first.UpdatedAt = t0
	if err := s.Put(ctx, first); err != nil {
		t.Fatalf("first Put() = %v", err)
	}

	t1 := t0.Add(time.Hour)
	second := binding("lark", "cli_app", "ou_alice", "user-alice", `["onex-x-max-tier"]`)
	second.Source = "ox-jobserver"
	second.UpdatedAt = t1
	if err := s.Put(ctx, second); err != nil {
		t.Fatalf("second Put() = %v", err)
	}

	var count int64
	if err := db.Model(&model.PeerAgentM{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("the triple holds %d rows, want 1: a repeated push inserted instead of updating", count)
	}

	got, err := s.GetByPeer(ctx, "lark", "cli_app", "ou_alice")
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if got.Agents != `["onex-x-max-tier"]` {
		t.Errorf("Agents = %q, want the second push to win", got.Agents)
	}
	if got.Source != "ox-jobserver" {
		t.Errorf("Source = %q, want the second writer recorded", got.Source)
	}
	// The row carries the time of its last write rather than of its first, which
	// is what makes "when was this binding last confirmed" answerable.
	if got.UpdatedAt.Unix() != t1.Unix() {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, t1)
	}
	// The identity columns are not in the upsert's SET list, so a conflict cannot
	// rewrite them: they are the key it conflicted on.
	if got.Canonical != "user-alice" {
		t.Errorf("Canonical = %q, want the identity left as the key wrote it", got.Canonical)
	}
}

// TestPeerAgentKeyIsTheTriple pins the third dimension. An open_id is scoped to
// the application that issued it, so the same person under a second Lark app is
// a different open_id — and even the same open_id under a second app must be a
// different row. Collapsing them is a silent identity mix-up, which is the
// failure the scope column exists to prevent.
func TestPeerAgentKeyIsTheTriple(t *testing.T) {
	ctx := context.Background()
	s, db := newPeerAgentStoreFor(t)

	if err := s.Put(ctx, binding("lark", "cli_app_one", "ou_1", "user-alice", `["a"]`)); err != nil {
		t.Fatalf("Put(app one) = %v", err)
	}
	if err := s.Put(ctx, binding("lark", "cli_app_two", "ou_1", "user-bob", `["b"]`)); err != nil {
		t.Fatalf("Put(app two) = %v", err)
	}
	// A different backend class, same scope and peer id.
	if err := s.Put(ctx, binding("openapi", "cli_app_one", "ou_1", "user-carol", `["c"]`)); err != nil {
		t.Fatalf("Put(openapi) = %v", err)
	}

	var count int64
	if err := db.Model(&model.PeerAgentM{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Fatalf("%d rows, want 3 — each dimension of the triple separates", count)
	}

	// And the index is real: a raw insert of a duplicate triple is refused, which
	// is what makes the ON CONFLICT above land on the intended row rather than on
	// some other unique index.
	dup := binding("lark", "cli_app_one", "ou_1", "user-alice", `["a"]`)
	if err := db.Create(dup).Error; err == nil {
		t.Error("inserting a duplicate triple succeeded, so the unique index is missing and the upsert has no target")
	}

	for _, tc := range []struct{ scope, want string }{
		{"cli_app_one", "user-alice"},
		{"cli_app_two", "user-bob"},
	} {
		got, err := s.GetByPeer(ctx, "lark", tc.scope, "ou_1")
		if err != nil {
			t.Fatalf("Get(%s) = %v", tc.scope, err)
		}
		if got == nil || got.Canonical != tc.want {
			t.Errorf("Get(lark, %s, ou_1) = %+v, want canonical %q", tc.scope, got, tc.want)
		}
	}
}

// TestPeerAgentPutFillsAMissingTimestamp pins that a caller which omits
// UpdatedAt means "now", not "the epoch". Every push from OneX omits it.
func TestPeerAgentPutFillsAMissingTimestamp(t *testing.T) {
	ctx := context.Background()
	s, _ := newPeerAgentStoreFor(t)

	before := time.Now()
	if err := s.Put(ctx, binding("lark", "cli_app", "ou_alice", "user-alice", `["a"]`)); err != nil {
		t.Fatalf("Put() = %v", err)
	}

	got, err := s.GetByPeer(ctx, "lark", "cli_app", "ou_alice")
	if err != nil || got == nil {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	if got.UpdatedAt.Before(before.Add(-time.Second)) || got.UpdatedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("UpdatedAt = %v, want a value near now (%v)", got.UpdatedAt, before)
	}
}

// TestPeerAgentWritesWithoutAKeyAreRefused pins the guards. A row keyed by an
// empty peer id is one no lookup can ever find, so it would be invisible garbage
// rather than a binding.
func TestPeerAgentWritesWithoutAKeyAreRefused(t *testing.T) {
	ctx := context.Background()
	s, db := newPeerAgentStoreFor(t)

	for _, m := range []*model.PeerAgentM{
		nil,
		binding("", "cli_app", "ou_alice", "user-alice", `["a"]`),
		binding("lark", "cli_app", "", "user-alice", `["a"]`),
	} {
		if err := s.Put(ctx, m); err != nil {
			t.Errorf("Put(%+v) = %v, want a silent no-op", m, err)
		}
	}

	var count int64
	if err := db.Model(&model.PeerAgentM{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("%d rows written for keys that cannot be looked up", count)
	}

	if got, err := s.GetByPeer(ctx, "", "", ""); err != nil || got != nil {
		t.Errorf("Get() with no key = %+v, %v, want a miss", got, err)
	}
}

// TestPeerAgentDeleteIsIdempotent pins that unbinding is the same outcome
// whether or not there was anything to unbind. An unbind is retried by whoever
// asked for it, so a second one must not be an error.
func TestPeerAgentDeleteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, db := newPeerAgentStoreFor(t)

	if err := s.Put(ctx, binding("lark", "cli_app", "ou_alice", "user-alice", `["a"]`)); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := s.DeleteByPeer(ctx, "lark", "cli_app", "ou_alice"); err != nil {
			t.Fatalf("Delete() #%d = %v", i+1, err)
		}
	}

	var count int64
	if err := db.Model(&model.PeerAgentM{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("%d rows left after unbinding", count)
	}
	if got, err := s.GetByPeer(ctx, "lark", "cli_app", "ou_alice"); err != nil || got != nil {
		t.Errorf("Get() after deletion = %+v, %v, want a miss", got, err)
	}
}

// TestPeerAgentDeleteIsScopedToTheTriple pins that unbinding one application
// does not unbind the same open_id under another — the same collision the
// unique key prevents on the way in.
func TestPeerAgentDeleteIsScopedToTheTriple(t *testing.T) {
	ctx := context.Background()
	s, db := newPeerAgentStoreFor(t)

	for _, scope := range []string{"cli_app_one", "cli_app_two"} {
		if err := s.Put(ctx, binding("lark", scope, "ou_1", "user-alice", `["a"]`)); err != nil {
			t.Fatalf("Put(%s) = %v", scope, err)
		}
	}
	if err := s.DeleteByPeer(ctx, "lark", "cli_app_one", "ou_1"); err != nil {
		t.Fatalf("Delete() = %v", err)
	}

	var count int64
	if err := db.Model(&model.PeerAgentM{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("%d rows left, want the other application's binding untouched", count)
	}
}

// TestPeerAgentStoreIsSafeForConcurrentReaders is the hot-path shape: the
// lookup runs on every inbound message while bindings are being pushed from
// outside. Nothing here asserts a lock — it asserts that no code path panics or
// corrupts under the arrangement the message path actually has.
func TestPeerAgentStoreIsSafeForConcurrentReaders(t *testing.T) {
	ctx := context.Background()
	s, _ := newPeerAgentStoreFor(t)

	if err := s.Put(ctx, binding("lark", "cli_app", "ou_alice", "user-alice", `["a"]`)); err != nil {
		t.Fatalf("Put() = %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if _, err := s.GetByPeer(ctx, "lark", "cli_app", "ou_alice"); err != nil {
				t.Errorf("concurrent Get: %v", err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			if err := s.Put(ctx, binding("lark", "cli_app", "ou_alice", "user-alice", `["b"]`)); err != nil {
				t.Errorf("concurrent Put: %v", err)
			}
		}(i)
	}
	wg.Wait()

	got, err := s.GetByPeer(ctx, "lark", "cli_app", "ou_alice")
	if err != nil || got == nil {
		t.Fatalf("Get() after the concurrent writes = %+v, %v", got, err)
	}
	if got.Agents != `["b"]` {
		t.Errorf("Agents = %q, want the last write", got.Agents)
	}
}
