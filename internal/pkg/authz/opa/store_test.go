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

package opa

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
)

// fakeRuleSource is a hand-written RuleSource returning a fixed set (or error).
type fakeRuleSource struct {
	rules []*model.AuthzRuleM
	err   error
}

func (f *fakeRuleSource) List(context.Context, *where.Options) (int64, []*model.AuthzRuleM, error) {
	return int64(len(f.rules)), f.rules, f.err
}

func TestRuleStoreSyncFiltersNamelessAndDisabledRows(t *testing.T) {
	store := NewRuleStore(&fakeRuleSource{rules: []*model.AuthzRuleM{
		{Name: "keep", Status: model.StatusEnabled},
		{Name: "", Status: model.StatusEnabled},
		{Name: "disabled", Status: model.StatusDisabled},
		nil,
	}})

	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	got := store.Snapshot()
	want := []model.AuthzRuleM{{Name: "keep", Status: model.StatusEnabled}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want only the enabled, named rule", got)
	}
}

func TestRuleStoreSyncReplacesTheSnapshotWholesale(t *testing.T) {
	src := &fakeRuleSource{rules: []*model.AuthzRuleM{{Name: "first", Status: model.StatusEnabled}}}
	store := NewRuleStore(src)
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	src.rules = []*model.AuthzRuleM{{Name: "second", Status: model.StatusEnabled}}
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if got := store.Snapshot(); len(got) != 1 || got[0].Name != "second" {
		t.Fatalf("got %+v, want the new rule set to fully replace the old one", got)
	}
}

func TestRuleStoreSyncKeepsThePreviousSnapshotOnFailure(t *testing.T) {
	src := &fakeRuleSource{rules: []*model.AuthzRuleM{{Name: "good", Status: model.StatusEnabled}}}
	store := NewRuleStore(src)
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	src.err = errors.New("database down")
	if _, err := store.Sync(context.Background()); err == nil {
		t.Fatal("expected Sync to report the load failure")
	}

	if got := store.Snapshot(); len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("got %+v, want the previous snapshot retained after a failed sync", got)
	}
}

func TestRuleStoreSnapshotIsADefensiveCopy(t *testing.T) {
	store := NewRuleStore(&fakeRuleSource{rules: []*model.AuthzRuleM{{Name: "rule", Status: model.StatusEnabled}}})
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	snap := store.Snapshot()
	snap[0].Name = "mutated"

	if got := store.Snapshot(); got[0].Name != "rule" {
		t.Fatalf("mutating a snapshot must not affect the store, got %q", got[0].Name)
	}
}

func TestRuleStoreWithoutASourceIsANoOp(t *testing.T) {
	store := NewRuleStore(nil)
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync with no source: %v", err)
	}
	if got := store.Snapshot(); len(got) != 0 {
		t.Fatalf("got %+v, want an empty snapshot", got)
	}
}

// capturingHandler records what the store logged, which is the only way to tell
// one warning from a warning per refresh.
type capturingHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	level   slog.Level
	message string
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, capturedRecord{level: r.Level, message: r.Message})
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingHandler) countAtLeast(level slog.Level) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.level >= level {
			n++
		}
	}
	return n
}

func (h *capturingHandler) messagesAt(level slog.Level) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.level == level {
			out = append(out, r.message)
		}
	}
	return out
}

// TestRuleStoreWarnsOncePerEmptyTransition pins the fix for a silent outage: an
// empty rule table denies every request through fail-closed, and the refresher
// runs every minute, so a warning per pass would turn one misconfiguration into
// a line a minute while saying nothing new after the first.
func TestRuleStoreWarnsOncePerEmptyTransition(t *testing.T) {
	handler := &capturingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	src := &fakeRuleSource{}
	store := NewRuleStore(src)

	// Three empty refreshes are one misconfiguration, not three.
	for range 3 {
		if _, err := store.Sync(context.Background()); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}
	if got := handler.countAtLeast(slog.LevelWarn); got != 1 {
		t.Errorf("an empty rule table logged %d warnings over 3 refreshes, want 1", got)
	}

	// Rules appearing is worth saying once, at a level nobody has to act on.
	src.rules = []*model.AuthzRuleM{{Name: "allow", Status: model.StatusEnabled}}
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := handler.countAtLeast(slog.LevelWarn); got != 1 {
		t.Errorf("rules appearing logged another %d warnings, want none", got-1)
	}
	if got := handler.messagesAt(slog.LevelInfo); len(got) != 1 {
		t.Errorf("rules appearing logged %d info lines, want exactly 1: %v", len(got), got)
	}

	// Losing them again is a new outage and has to be reported again.
	src.rules = nil
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := handler.countAtLeast(slog.LevelWarn); got != 2 {
		t.Errorf("losing the rules again brought the total to %d warnings, want 2", got)
	}
}

// TestRuleStoreWithoutRulesButAlsoWithoutASourceStaysQuiet pins the other
// configuration: a deployment that never wired a rule source allows by default
// rather than denying, so its empty store must not be reported as an outage.
func TestRuleStoreWithoutRulesButAlsoWithoutASourceStaysQuiet(t *testing.T) {
	handler := &capturingHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if _, err := NewRuleStore(nil).Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := handler.countAtLeast(slog.LevelWarn); got != 0 {
		t.Errorf("a store with no source logged %d warnings, want none", got)
	}
}

// TestRuleStoreSyncReportsWhetherTheRuleSetChanged pins the signal the reload
// path uses to decide whether cached decisions are worth dropping: an unchanged
// set must report false (a timer firing is not a change), and any edit must
// report true (otherwise a revoked grant survives the whole cache TTL).
func TestRuleStoreSyncReportsWhetherTheRuleSetChanged(t *testing.T) {
	rule := func(name, effect string) *model.AuthzRuleM {
		return &model.AuthzRuleM{Name: name, SubjectType: "user", SubjectID: []string{"u1"},
			ResourceKind: "agent", ResourceName: []string{"a"}, Verbs: []string{"read"},
			Effect: effect, Status: model.StatusEnabled}
	}
	source := &fakeRuleSource{rules: []*model.AuthzRuleM{rule("r1", "allow")}}
	store := NewRuleStore(source)
	ctx := context.Background()

	changed, err := store.Sync(ctx)
	if err != nil || !changed {
		t.Fatalf("first sync: changed=%v err=%v, want true/nil", changed, err)
	}

	changed, err = store.Sync(ctx)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if changed {
		t.Fatal("a sync that loaded the identical rule set reported a change; every refresh would then clear the result cache")
	}

	// Same rule name, different effect: the edit that a name-only comparison
	// would miss.
	source.rules = []*model.AuthzRuleM{rule("r1", "deny")}
	changed, err = store.Sync(ctx)
	if err != nil {
		t.Fatalf("third sync: %v", err)
	}
	if !changed {
		t.Fatal("an edited rule reported no change; the cached answer computed under the old effect would survive")
	}

	// A failing source is a no-op, and must not claim a change either.
	source.err = context.DeadlineExceeded
	changed, err = store.Sync(ctx)
	if err == nil {
		t.Fatal("expected the source error to propagate")
	}
	if changed {
		t.Fatal("a failed sync reported a change")
	}
}
