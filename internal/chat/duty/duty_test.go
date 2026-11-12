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

package duty

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/userdir"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	dutyprovider "github.com/onexstack/onexai/pkg/duty"
)

// fakeSource is a Source whose answer the test controls.
type fakeSource struct {
	groups []Group
	err    error
	// rosterErr is what RosterError reports: a source that loaded its module
	// metadata while the rosters behind them stayed unreadable.
	rosterErr error
	// calls counts Load invocations.
	calls int
}

func (f *fakeSource) Load(_ context.Context) ([]Group, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.groups, nil
}

func (f *fakeSource) RosterError() error { return f.rosterErr }

// newTestStore builds a source-backed store with a refresh interval long enough
// that only the explicit calls in the test matter, and waits for the first load.
//
// The load moved off the constructor so a slow roster backend cannot hold up
// start-up; that makes it the test's job to wait for it, and waiting on the
// store's own signal is what keeps this deterministic instead of a sleep.
func newTestStore(t *testing.T, src Source) *Store {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := NewWithSource(ctx, src, time.Hour)
	select {
	case <-s.firstLoadDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the store did not finish its first load")
	}
	return s
}

func TestNewWithSourceLoadsGroups(t *testing.T) {
	src := &fakeSource{groups: []Group{
		{ID: "onex", Name: "onex", Watchers: []*v1.Watcher{{UserId: "go.kong", UserName: "go.kong@example.com"}}},
		{ID: "obs", Name: "obs", Watchers: []*v1.Watcher{{UserId: "ops.luo", UserName: "ops.luo@example.com"}}},
	}}
	s := newTestStore(t, src)

	if !s.HasGroup("onex") || !s.HasGroup("ONEX") {
		t.Fatalf("HasGroup is case-insensitive and should find onex")
	}
	if got := s.GetPrimaryOncaller("onex"); got.GetUserId() != "go.kong" {
		t.Errorf("GetPrimaryOncaller(onex) = %+v, want go.kong", got)
	}
	if got := s.GetOnDutys("obs"); len(got) != 1 || got[0].GetUserId() != "ops.luo" {
		t.Errorf("GetOnDutys(obs) = %+v, want ops.luo", got)
	}
	if groups := s.ListGroup(); len(groups) != 2 {
		t.Errorf("ListGroup() has %d groups, want 2", len(groups))
	}
}

// TestInitialLoadFailureIsTolerated pins the deliberate difference from
// dnswhitelist: a source that is down at start-up must not stop the server.
func TestInitialLoadFailureIsTolerated(t *testing.T) {
	src := &fakeSource{err: errors.New("connection refused")}
	s := newTestStore(t, src)

	if s.HasGroup("onex") {
		t.Fatal("a failed initial load should leave the store empty")
	}
	if got := s.GetOnDutys("onex"); len(got) != 0 {
		t.Fatalf("GetOnDutys on an empty store = %+v, want none", got)
	}

	// The loop recovers on a later tick, which the test drives by hand.
	src.err = nil
	src.groups = []Group{{ID: "onex", Name: "onex", Watchers: []*v1.Watcher{{UserId: "go.kong"}}}}
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if !s.HasGroup("onex") {
		t.Fatal("Refresh should have loaded the group once the source recovered")
	}
}

// TestStoreHasKnownGroups pins the distinction the duty commands branch on:
// "nothing was loaded" versus "the name is not one of ours". ActiveGroupInfos
// and HasGroup cannot make it, because a group with no watcher satisfies
// neither.
func TestStoreHasKnownGroups(t *testing.T) {
	if s := newTestStore(t, &fakeSource{}); s.HasKnownGroups() {
		t.Error("an empty store reports known groups")
	}

	s := newTestStore(t, &fakeSource{groups: []Group{{ID: "onex", Name: "onex"}}})
	if !s.HasKnownGroups() {
		t.Error("a loaded group with no rotation is still a known group")
	}
	if s.HasGroup("onex") {
		t.Error("HasGroup is watcher-based; a group with no rotation must not satisfy it")
	}

	// A group that only ever arrived through /assign has no meta entry.
	assigned := newTestStore(t, &fakeSource{})
	assigned.SetOncall("eks", []*v1.Watcher{{UserId: "go.kong"}})
	if !assigned.HasKnownGroups() {
		t.Error("a locally assigned group is store data too")
	}
}

// TestStoreReportsUnreadableRosters pins the state a roster backend outage
// leaves behind: the module metadata is loaded, no group is known to be
// staffed, and the store can still tell "nobody is on call" from "the rosters
// could not be read". Without the verdict the two look identical to a command,
// and the second one is answered as if the deployment had no modules.
func TestStoreReportsUnreadableRosters(t *testing.T) {
	src := &fakeSource{
		groups:    []Group{{ID: "go", Name: "Go 开发"}},
		rosterErr: errors.New("connection refused"),
	}
	s := newTestStore(t, src)

	if s.RosterError() == nil {
		t.Fatal("RosterError() = nil, want the source's failure")
	}
	if !s.HasKnownGroups() {
		t.Error("the module metadata loaded, so the group is known")
	}
	if got := s.ActiveGroupInfos(); len(got) != 0 {
		t.Errorf("ActiveGroupInfos() = %+v, want none: no roster was read", got)
	}

	// The next pass that reads the rosters clears the verdict; a store that
	// kept it would go on calling itself unavailable after it recovered.
	src.rosterErr = nil
	src.groups = []Group{{ID: "go", Name: "Go 开发", Watchers: []*v1.Watcher{{UserId: "go.kong"}}}}
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if err := s.RosterError(); err != nil {
		t.Errorf("RosterError() = %v, want nil after a pass that read the rosters", err)
	}
	if !s.HasGroup("go") {
		t.Error("the readable roster should have made the group active")
	}
}

// TestRefreshKeepsRostersItCouldNotRead is the trade-off the duty commands take
// during a roster outage. The watcher lists a failed pass carries are unknown,
// not empty, so the last readable ones are kept: paging whoever was on call a
// few minutes ago beats paging nobody, and the next readable pass replaces them
// with the truth — including an empty list, which is then a fact.
func TestRefreshKeepsRostersItCouldNotRead(t *testing.T) {
	src := &fakeSource{groups: []Group{
		{ID: "go", Name: "Go 开发", Watchers: []*v1.Watcher{{UserId: "ops.luo"}}},
	}}
	s := newTestStore(t, src)

	// The backend goes away: the module metadata still comes back, the rosters
	// do not. kubernetes is new, so it has no earlier roster to fall back on.
	src.groups = []Group{
		{ID: "go", Name: "Go 开发"},
		{ID: "kubernetes", Name: "Kubernetes"},
	}
	src.rosterErr = errors.New("connection refused")
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}

	if got := s.GetPrimaryOncaller("go"); got.GetUserId() != "ops.luo" {
		t.Errorf("GetPrimaryOncaller(go) = %+v, want the last readable roster", got)
	}
	if s.RosterError() == nil {
		t.Error("the pass must still be reported as a roster failure")
	}
	if s.HasGroup("kubernetes") {
		t.Error("a group whose roster was never read must not become active")
	}

	// The backend answers again and says go has no rotation right now. That
	// empty list is an answer, so it replaces the kept one.
	src.rosterErr = nil
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if got := s.GetOnDutys("go"); len(got) != 0 {
		t.Errorf("GetOnDutys(go) = %+v, want the authoritative empty roster", got)
	}
	if s.RosterError() != nil {
		t.Errorf("RosterError() = %v, want nil after a readable pass", s.RosterError())
	}
}

// TestStoreDegradedCoversBothFailureModes pins what makes the refresh loop
// retry early. A load that fails outright counts, even though it keeps the
// previous assignments, and so does a load whose rosters could not be read.
func TestStoreDegradedCoversBothFailureModes(t *testing.T) {
	healthy := &fakeSource{groups: []Group{{ID: "onex", Name: "onex"}}}
	if s := newTestStore(t, healthy); s.degraded() {
		t.Error("a complete load must not mark the store degraded")
	}

	failing := &fakeSource{err: errors.New("db down")}
	s := newTestStore(t, failing)
	if !s.degraded() {
		t.Error("a failed load must mark the store degraded")
	}

	// Recovery clears it, so the loop goes back to the ordinary interval.
	failing.err = nil
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if s.degraded() {
		t.Error("a complete load must clear the degraded state")
	}

	rosters := newTestStore(t, &fakeSource{rosterErr: errors.New("connection refused")})
	if !rosters.degraded() {
		t.Error("a load whose rosters could not be read must mark the store degraded")
	}
}

// TestNextRetryBacksOffAndCaps pins the retry policy that keeps a startup blip
// from blanking the duty commands for a full interval: consecutive incomplete
// refreshes back off from refreshRetryInterval, and never past the interval.
func TestNextRetryBacksOffAndCaps(t *testing.T) {
	s := &Store{refreshInterval: 5 * time.Minute}

	prev := time.Duration(0)
	want := []time.Duration{
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		5 * time.Minute,
		5 * time.Minute,
	}
	for i, want := range want {
		prev = s.nextRetry(prev)
		if prev != want {
			t.Fatalf("backoff step %d = %v, want %v", i, prev, want)
		}
	}

	// An interval shorter than the retry floor is honoured, so a caller cannot
	// configure a wait the loop then ignores.
	short := &Store{refreshInterval: 10 * time.Second}
	if got := short.nextRetry(0); got != 10*time.Second {
		t.Errorf("nextRetry with a 10s interval = %v, want it capped to the interval", got)
	}
}

// TestLocalAssignOutranksSource is the /assign contract: an operator's
// assignment is not undone by the next refresh.
func TestLocalAssignOutranksSource(t *testing.T) {
	src := &fakeSource{groups: []Group{
		{ID: "onex", Name: "onex", Watchers: []*v1.Watcher{{UserId: "go.kong"}}},
	}}
	s := newTestStore(t, src)

	s.SetOncall("onex", []*v1.Watcher{{UserId: "manual.user"}})

	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if got := s.GetPrimaryOncaller("onex"); got.GetUserId() != "manual.user" {
		t.Fatalf("GetPrimaryOncaller(onex) = %+v, want the locally assigned manual.user", got)
	}

	// A different group is still refreshed from the source.
	src.groups = append(src.groups, Group{ID: "obs", Name: "obs", Watchers: []*v1.Watcher{{UserId: "ops.luo"}}})
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if !s.HasGroup("obs") {
		t.Fatal("Refresh should still load groups that were not locally assigned")
	}
}

// TestRefreshDropsGroupsTheSourceDropped keeps the refresh convergent: a
// schedule deleted in Regen has to stop answering, not linger for the life of
// the process.
func TestRefreshDropsGroupsTheSourceDropped(t *testing.T) {
	src := &fakeSource{groups: []Group{
		{ID: "onex", Name: "onex", Watchers: []*v1.Watcher{{UserId: "go.kong"}}},
		{ID: "obs", Name: "obs", Watchers: []*v1.Watcher{{UserId: "ops.luo"}}},
	}}
	s := newTestStore(t, src)

	// obs is deleted in Regen, and onex is locally assigned so it must survive
	// regardless of what the source says.
	s.SetOncall("onex", []*v1.Watcher{{UserId: "manual.user"}})
	src.groups = []Group{{ID: "go", Name: "go", Watchers: []*v1.Watcher{{UserId: "toka.wu"}}}}

	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}

	if s.HasGroup("obs") {
		t.Error("obs was dropped by the source but is still known")
	}
	if !s.HasGroup("onex") {
		t.Error("the locally assigned group must survive a refresh")
	}
	if !s.HasGroup("go") {
		t.Error("go was added by the source but is not known")
	}
	if got := s.ListGroup(); len(got) != 2 {
		t.Errorf("ListGroup() has %d groups, want onex and go", len(got))
	}
}

// fakeRoster implements duty.Roster for the source tests.
type fakeRoster struct {
	schedules []dutyprovider.Schedule
	oncall    map[string]string
	err       error
	// oncallErr fails only the per-schedule read, which is the shape of a
	// backend that answers the listing and then breaks.
	oncallErr error
}

func (f *fakeRoster) Schedules(_ context.Context) ([]dutyprovider.Schedule, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.schedules, nil
}

func (f *fakeRoster) OnCall(_ context.Context, scheduleID string) (dutyprovider.Person, error) {
	if f.oncallErr != nil {
		return dutyprovider.Person{}, f.oncallErr
	}
	if f.err != nil {
		return dutyprovider.Person{}, f.err
	}
	name := f.oncall[scheduleID]
	return dutyprovider.Person{ID: name, Name: name}, nil
}

func TestRegenSourceMapsSchedulesToWatchers(t *testing.T) {
	cli := &fakeRoster{
		schedules: []dutyprovider.Schedule{
			{ID: "s1", Name: "OBS"},
			{ID: "s2", Name: "kubernetes"},
			{ID: "s3", Name: "aiinfra"},
		},
		oncall: map[string]string{
			"s1": "ops.luo@example.com",
			"s2": "go.kong@example.com",
			"s3": "", // nobody on call
		},
	}
	src := NewProviderSource(cli, userdir.Static{
		"ops.luo@example.com": "ops.luo",
		"go.kong@example.com": "go.kong",
	})

	groups, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	byID := map[string]Group{}
	for _, g := range groups {
		byID[g.ID] = g
	}

	// The module id is lower-cased because the command layer hands the store
	// a lower-cased module name.
	if _, ok := byID["obs"]; !ok {
		t.Fatalf("schedule OBS was not normalised to obs: %+v", byID)
	}
	if got := byID["obs"].Watchers[0]; got.GetUserId() != "ops.luo" || got.GetUserName() != "ops.luo@example.com" {
		t.Errorf("obs watcher = %+v, want the mapped Lark user_id with the email as the display name", got)
	}
	if got := byID["kubernetes"].Watchers[0].GetUserId(); got != "go.kong" {
		t.Errorf("kubernetes watcher user_id = %q, want go.kong", got)
	}
	if len(byID["aiinfra"].Watchers) != 0 {
		t.Errorf("aiinfra has %d watchers, want none for an empty on-call", len(byID["aiinfra"].Watchers))
	}
}

func TestRegenSourceFallsBackToRawName(t *testing.T) {
	cli := &fakeRoster{
		schedules: []dutyprovider.Schedule{{ID: "s1", Name: "go"}},
		oncall:    map[string]string{"s1": "unmapped@example.com"},
	}
	src := NewProviderSource(cli, userdir.Static{})

	groups, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := groups[0].Watchers[0].GetUserId(); got != "unmapped@example.com" {
		t.Errorf("watcher user_id = %q, want the raw name when no mapping exists", got)
	}
}
