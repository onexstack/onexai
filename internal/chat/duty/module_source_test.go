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

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/userdir"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	dutyprovider "github.com/onexstack/onexai/pkg/duty"
)

// fakeModuleLister is a moduleLister whose answer the test controls.
type fakeModuleLister struct {
	modules []*model.OncallGroupM
	err     error
}

func (f *fakeModuleLister) ListAll(_ context.Context) ([]*model.OncallGroupM, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.modules, nil
}

// fakeRoster lives in duty_test.go and is shared here.

func TestModuleSourceJoinsScheduleByID(t *testing.T) {
	src := NewModuleSource(
		&fakeModuleLister{modules: []*model.OncallGroupM{
			{Name: "ONEX", DisplayName: "onex 业务", ScheduleID: "sch-1", ShortDesc: "一站式平台", Order: 2},
		}},
		&fakeRoster{
			schedules: []dutyprovider.Schedule{{ID: "sch-1", Name: "onex"}},
			oncall:    map[string]string{"sch-1": "go.kong@example.com"},
		},
		userdir.Static{"go.kong@example.com": "go.kong"},
	)

	groups, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Load() returned %d groups, want 1", len(groups))
	}
	g := groups[0]
	if g.ID != "onex" {
		t.Errorf("ID = %q, want lower-cased %q", g.ID, "onex")
	}
	if g.Name != "onex 业务" {
		t.Errorf("Name = %q, want the display name", g.Name)
	}
	if g.ScheduleID != "sch-1" || g.ShortDesc != "一站式平台" || g.Order != 2 {
		t.Errorf("metadata not carried through: %+v", g)
	}
	if len(g.Watchers) != 1 || g.Watchers[0].GetUserId() != "go.kong" {
		t.Errorf("Watchers = %+v, want the mapped Lark user_id", g.Watchers)
	}
}

func TestModuleSourceWithoutScheduleIsInactive(t *testing.T) {
	src := NewModuleSource(
		&fakeModuleLister{modules: []*model.OncallGroupM{
			{Name: "onex", DisplayName: "onex 业务"},
			{Name: "obs", DisplayName: "观测", ScheduleID: "sch-2"},
		}},
		&fakeRoster{schedules: []dutyprovider.Schedule{{ID: "sch-2"}}, oncall: map[string]string{"sch-2": "ops.luo"}},
		nil,
	)

	s := newTestStore(t, src)
	if _, ok := s.GetGroupInfo("onex"); !ok {
		t.Fatal("a module without a schedule must still be known")
	}
	if s.HasGroup("onex") {
		t.Error("a module without a schedule must not be active")
	}
	active := s.ActiveGroupInfos()
	if len(active) != 1 || active[0].ID != "obs" {
		t.Errorf("ActiveGroupInfos() = %+v, want only obs", active)
	}
}

func TestModuleSourceDisplayNameFallsBackToName(t *testing.T) {
	src := NewModuleSource(
		&fakeModuleLister{modules: []*model.OncallGroupM{{Name: "bke", ScheduleID: "s"}}},
		&fakeRoster{schedules: []dutyprovider.Schedule{{ID: "s"}}, oncall: map[string]string{"s": "someone"}},
		nil,
	)
	groups, _ := src.Load(context.Background())
	if len(groups) != 1 || groups[0].Name != "bke" {
		t.Fatalf("Name should fall back to the module id, got %+v", groups)
	}
}

func TestModuleSourceExcludesOrphanSchedules(t *testing.T) {
	src := NewModuleSource(
		&fakeModuleLister{modules: []*model.OncallGroupM{{Name: "onex", ScheduleID: "s1"}}},
		&fakeRoster{
			schedules: []dutyprovider.Schedule{{ID: "s1", Name: "onex"}, {ID: "s2", Name: "leftover"}},
			oncall:    map[string]string{"s1": "a", "s2": "b"},
		},
		nil,
	)
	groups, _ := src.Load(context.Background())
	if len(groups) != 1 || groups[0].ID != "onex" {
		t.Fatalf("orphan schedules must be excluded by default, got %+v", groups)
	}
}

// A group whose schedule_id has not been backfilled still resolves its roster
// from the Regen schedule that shares its id — the state the live group
// table is in while its rows are being created.
func TestModuleSourceResolvesRosterByNameWhenScheduleIDEmpty(t *testing.T) {
	src := NewModuleSource(
		&fakeModuleLister{modules: []*model.OncallGroupM{{Name: "GO", DisplayName: "Go 语言"}}},
		&fakeRoster{
			schedules: []dutyprovider.Schedule{{ID: "sch-go", Name: "go"}},
			oncall:    map[string]string{"sch-go": "ops.luo@example.com"},
		},
		userdir.Static{"ops.luo@example.com": "ops.luo"},
	)

	groups, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Load() returned %d groups, want 1", len(groups))
	}
	g := groups[0]
	if g.ID != "go" || g.ScheduleID != "sch-go" {
		t.Errorf("group = %+v, want the id go backed by the same-named schedule", g)
	}
	if len(g.Watchers) != 1 || g.Watchers[0].GetUserId() != "ops.luo" {
		t.Errorf("Watchers = %+v, want the mapped Lark user_id", g.Watchers)
	}
}

func TestModuleSourceExplicitScheduleIDWinsOverName(t *testing.T) {
	src := NewModuleSource(
		&fakeModuleLister{modules: []*model.OncallGroupM{{Name: "go", ScheduleID: "sch-explicit"}}},
		&fakeRoster{
			schedules: []dutyprovider.Schedule{{ID: "sch-explicit", Name: "other"}, {ID: "sch-named", Name: "go"}},
			oncall:    map[string]string{"sch-explicit": "explicit@x", "sch-named": "named@x"},
		},
		nil,
	)
	groups, _ := src.Load(context.Background())
	if len(groups) != 1 || groups[0].ScheduleID != "sch-explicit" {
		t.Fatalf("groups = %+v, want the explicit schedule to win", groups)
	}
	if len(groups[0].Watchers) != 1 || groups[0].Watchers[0].GetUserName() != "explicit@x" {
		t.Errorf("Watchers = %+v, want the explicit schedule's roster", groups[0].Watchers)
	}
}

func TestModuleSourceDanglingScheduleIDDoesNotFallBackToName(t *testing.T) {
	src := NewModuleSource(
		&fakeModuleLister{modules: []*model.OncallGroupM{{Name: "go", ScheduleID: "missing"}}},
		&fakeRoster{
			schedules: []dutyprovider.Schedule{{ID: "sch-go", Name: "go"}},
			oncall:    map[string]string{"sch-go": "ops.luo@example.com"},
		},
		nil,
	)
	groups, _ := src.Load(context.Background())
	if len(groups) != 1 {
		t.Fatalf("Load() returned %d groups, want 1", len(groups))
	}
	if len(groups[0].Watchers) != 0 {
		t.Errorf("an explicit but unknown schedule_id must not be replaced by a same-named schedule, got %+v", groups[0].Watchers)
	}
	// A module pointing at a schedule that is gone is a defect to fix, not a
	// roster backend that could not be read: the commands must keep answering
	// "no rotation" for it instead of declaring the whole service unavailable.
	if err := rosterErrorOf(src); err != nil {
		t.Errorf("roster verdict = %v, want nil for a dangling schedule_id", err)
	}
}

// TestModuleSourceReportsUnreadableRosters is the outage the duty commands have
// to recognise: the module table loads, Regen does not, so every group comes
// back without a watcher and the source says why. The store turns that into the
// service-unavailable answer instead of "there are no modules".
func TestModuleSourceReportsUnreadableRosters(t *testing.T) {
	mods := &fakeModuleLister{modules: []*model.OncallGroupM{{Name: "go", ScheduleID: "sch-go"}}}
	cli := &fakeRoster{err: errors.New("connection refused")}
	src := NewModuleSource(mods, cli, nil)

	groups, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v, want the module metadata to survive a roster outage", err)
	}
	if len(groups) != 1 || len(groups[0].Watchers) != 0 {
		t.Fatalf("Load() = %+v, want the group listed without watchers", groups)
	}
	if err := rosterErrorOf(src); err == nil {
		t.Error("roster verdict = nil, want the schedules failure")
	}

	// The next readable pass clears it, so a store that recovered stops saying
	// it cannot tell who is on call.
	cli.err = nil
	cli.schedules = []dutyprovider.Schedule{{ID: "sch-go", Name: "go"}}
	cli.oncall = map[string]string{"sch-go": "ops.luo@example.com"}
	if _, err := src.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := rosterErrorOf(src); err != nil {
		t.Errorf("roster verdict = %v, want nil after a readable pass", err)
	}
}

// TestModuleSourceOncallReadFailureIsARosterFailure covers the other half of an
// outage: the listing answers, a per-schedule read does not. That is still the
// backend failing, so no group's roster can be vouched for.
func TestModuleSourceOncallReadFailureIsARosterFailure(t *testing.T) {
	src := NewModuleSource(
		&fakeModuleLister{modules: []*model.OncallGroupM{{Name: "go", ScheduleID: "sch-go"}}},
		&fakeRoster{
			schedules: []dutyprovider.Schedule{{ID: "sch-go", Name: "go"}},
			oncallErr: errors.New("503 from regen"),
		},
		nil,
	)

	groups, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v, want the failure to stay per-schedule", err)
	}
	if len(groups) != 1 || len(groups[0].Watchers) != 0 {
		t.Fatalf("Load() = %+v, want the group listed without watchers", groups)
	}
	if err := rosterErrorOf(src); err == nil {
		t.Error("roster verdict = nil, want the on-call read failure")
	}
}

func TestModuleSourceDuplicateScheduleNamesKeepTheFirst(t *testing.T) {
	src := NewModuleSource(
		&fakeModuleLister{modules: []*model.OncallGroupM{{Name: "go"}}},
		&fakeRoster{
			schedules: []dutyprovider.Schedule{{ID: "sch-1", Name: "go"}, {ID: "sch-2", Name: "go"}},
			oncall:    map[string]string{"sch-1": "first@x", "sch-2": "second@x"},
		},
		nil,
	)
	groups, _ := src.Load(context.Background())
	if len(groups) != 1 || groups[0].ScheduleID != "sch-1" {
		t.Fatalf("groups = %+v, want the first same-named schedule", groups)
	}
	if len(groups[0].Watchers) != 1 || groups[0].Watchers[0].GetUserName() != "first@x" {
		t.Errorf("Watchers = %+v, want the first schedule's roster", groups[0].Watchers)
	}
}

// An empty module table is the live state before the module rows exist; the
// source falls back to the Regen schedules so the duty commands keep answering.
func TestModuleSourceFallsBackToSchedulesWhenModuleTableIsEmpty(t *testing.T) {
	src := NewModuleSource(
		&fakeModuleLister{},
		&fakeRoster{
			schedules: []dutyprovider.Schedule{{ID: "sch-go", Name: "go"}, {ID: "sch-onex", Name: "onex"}},
			oncall:    map[string]string{"sch-go": "ops.luo@example.com", "sch-onex": "go.kong@example.com"},
		},
		nil,
	)

	s := newTestStore(t, src)
	if !s.HasGroup("go") {
		t.Fatal("an empty module table must fall back to the regen schedules")
	}
	if got := s.GetPrimaryOncaller("go"); got.GetUserName() != "ops.luo@example.com" {
		t.Errorf("GetPrimaryOncaller(go) = %+v, want the schedule's on-call user", got)
	}
	if got := s.ActiveGroupInfos(); len(got) != 2 {
		t.Errorf("ActiveGroupInfos() = %+v, want both schedules", got)
	}
}

// The other side of that fallback: nothing in the table and nothing in Regen
// leaves no group at all — the state the duty commands report as the service
// being unavailable. (The live incident: an empty table plus a rejected Regen
// login.)
func TestModuleSourceEmptyTableAndNoSchedulesYieldsNoGroups(t *testing.T) {
	src := NewModuleSource(&fakeModuleLister{}, &fakeRoster{}, nil)

	s := newTestStore(t, src)
	if s.HasKnownGroups() {
		t.Fatalf("expected no groups, got %+v", s.ListGroup())
	}
	if got := s.ActiveGroupInfos(); len(got) != 0 {
		t.Errorf("ActiveGroupInfos() = %+v, want none", got)
	}
}

func TestModuleSourceFallsBackToRegenWhenListerNil(t *testing.T) {
	src := NewModuleSource(nil,
		&fakeRoster{schedules: []dutyprovider.Schedule{{ID: "s1", Name: "onex"}}, oncall: map[string]string{"s1": "alice"}},
		nil,
	)
	groups, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(groups) != 1 || groups[0].ID != "onex" || len(groups[0].Watchers) != 1 {
		t.Fatalf("expected the regen-only fallback to list schedules, got %+v", groups)
	}
}

func TestStoreFilterGroups(t *testing.T) {
	s := newTestStore(t, &fakeSource{groups: []Group{
		{ID: "onex", Name: "onex 业务", ShortDesc: "一站式平台", Watchers: []*v1.Watcher{{UserId: "a"}}},
		{ID: "obs", Name: "观测", LongDesc: "monitor and alerting", Watchers: []*v1.Watcher{{UserId: "b"}}},
	}})

	got, ok := s.FilterGroups("moni")
	if !ok {
		t.Fatal("FilterGroups reported a bad regex for a valid pattern")
	}
	if len(got) != 1 || got[0].ID != "obs" {
		t.Errorf("filter 'moni' should match obs via LongDesc, got %+v", got)
	}

	all, ok := s.FilterGroups("")
	if !ok || len(all) != 2 {
		t.Errorf("empty filter should return every active group, got %+v", all)
	}

	if _, ok := s.FilterGroups("("); ok {
		t.Error("FilterGroups should report a bad regex")
	}
}

func TestStoreGroupInfosOrdersByOrderThenID(t *testing.T) {
	s := newTestStore(t, &fakeSource{groups: []Group{
		{ID: "zeta", Name: "zeta", Order: 1, Watchers: []*v1.Watcher{{UserId: "a"}}},
		{ID: "alpha", Name: "alpha", Order: 1, Watchers: []*v1.Watcher{{UserId: "b"}}},
		{ID: "first", Name: "first", Order: 0, Watchers: []*v1.Watcher{{UserId: "c"}}},
	}})
	got := s.GroupInfos()
	want := []string{"first", "alpha", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("GroupInfos() returned %d groups, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("GroupInfos()[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
}

type fakeScheduleLister struct {
	aggs []*model.OncallScheduleAggregate
}

func (f *fakeScheduleLister) ListAggregates(context.Context) ([]*model.OncallScheduleAggregate, error) {
	return f.aggs, nil
}

func TestNativeScheduleWinsOverLegacyScheduleID(t *testing.T) {
	start := time.Now().UTC().Add(-24 * time.Hour)
	schedules := &fakeScheduleLister{aggs: []*model.OncallScheduleAggregate{{
		Schedule: &model.OncallScheduleM{GroupName: "go", Timezone: "UTC"},
		Layers: []model.OncallScheduleLayerAggregate{{
			Layer: model.OncallScheduleLayerM{
				Name:                 "Primary",
				RotationStart:        start,
				ShiftDurationSeconds: 7 * 86400,
			},
			Participants: []*model.OncallScheduleParticipantM{{UserName: "native@example.com"}},
		}},
	}}}
	provider := &fakeRoster{
		schedules: []dutyprovider.Schedule{{ID: "sch-legacy", Name: "go"}},
		oncall:    map[string]string{"sch-legacy": "legacy@example.com"},
	}
	src := NewModuleSourceWithSchedules(
		&fakeModuleLister{modules: []*model.OncallGroupM{{Name: "go", ScheduleID: "sch-legacy"}}},
		schedules, provider, nil,
	)

	groups, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Load() returned %d groups, want 1", len(groups))
	}
	if groups[0].ScheduleSource != "native" {
		t.Errorf("ScheduleSource = %q, want native", groups[0].ScheduleSource)
	}
	if len(groups[0].Watchers) != 1 || groups[0].Watchers[0].UserName != "native@example.com" {
		t.Errorf("Watchers = %v, want the native schedule participant", groups[0].Watchers)
	}
}

func TestLegacyGroupStillUsesProvider(t *testing.T) {
	provider := &fakeRoster{
		schedules: []dutyprovider.Schedule{{ID: "sch-legacy", Name: "legacy"}},
		oncall:    map[string]string{"sch-legacy": "legacy@example.com"},
	}
	src := NewModuleSourceWithSchedules(
		&fakeModuleLister{modules: []*model.OncallGroupM{{Name: "legacy", ScheduleID: "sch-legacy"}}},
		&fakeScheduleLister{}, provider, nil,
	)

	groups, err := src.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Load() returned %d groups, want 1", len(groups))
	}
	if groups[0].ScheduleSource != "regen" {
		t.Errorf("ScheduleSource = %q, want regen", groups[0].ScheduleSource)
	}
	if len(groups[0].Watchers) != 1 || groups[0].Watchers[0].UserName != "legacy@example.com" {
		t.Errorf("Watchers = %v, want the provider participant", groups[0].Watchers)
	}
}
