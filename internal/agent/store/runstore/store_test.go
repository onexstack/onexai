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

package runstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/onexstack/onexai/internal/agent/model"
)

// newTestStore opens a private in-memory database with the admission table.
//
// The store's lease comparisons run on the database clock, so the tests need a
// real database. The sqlite driver is a stub without cgo; skipping there is
// honest, and CI builds with cgo so the cases still run.
func newTestStore(t *testing.T) *SQLStore {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		if strings.Contains(err.Error(), "requires cgo") || strings.Contains(err.Error(), "stub") {
			t.Skipf("sqlite requires cgo: %v", err)
		}
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap *sql.DB: %v", err)
	}
	// One connection keeps the in-memory database visible to every statement.
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.AutoMigrate(&model.RunAdmissionM{}, &model.RunM{}, &model.SessionM{}); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	return New(db)
}

func pendingAdmission(t *testing.T, s *SQLStore, id string, scheduled int64) *model.RunAdmissionM {
	t.Helper()
	admission := &model.RunAdmissionM{
		ID:            id,
		SessionID:     "session-" + id,
		ScheduledTime: scheduled,
		State:         model.RunStatePending,
		CreatedAt:     time.Now().UnixMilli(),
	}
	if _, _, err := s.AdmitRun(context.Background(), admission); err != nil {
		t.Fatalf("admit %s: %v", id, err)
	}
	return admission
}

func TestClaimAdmissionSingleWinner(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pendingAdmission(t, store, "a1", 0)

	first, err := store.ClaimAdmission(ctx, "a1", "owner-1", time.Minute, 5)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if first != ClaimAcquired {
		t.Fatalf("first claim = %v, want ClaimAcquired", first)
	}

	second, err := store.ClaimAdmission(ctx, "a1", "owner-2", time.Minute, 5)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second != ClaimNotAcquired {
		t.Fatalf("second claim = %v, want ClaimNotAcquired while the lease is live", second)
	}

	row, err := store.GetAdmission(ctx, "a1")
	if err != nil {
		t.Fatalf("get admission: %v", err)
	}
	if row.LeaseOwner != "owner-1" || row.ClaimCount != 1 || row.State != model.RunStateRunning {
		t.Fatalf("row = %+v, want owner-1 running with claim_count 1", row)
	}
}

func TestClaimAdmissionReclaimsExpiredLease(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pendingAdmission(t, store, "a2", 0)

	if _, err := store.ClaimAdmission(ctx, "a2", "owner-1", time.Minute, 5); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Simulate the owner dying: its lease expires without being renewed.
	if err := store.db.Model(&model.RunAdmissionM{}).
		Where("id = ?", "a2").Update("lease_until", 0).Error; err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	result, err := store.ClaimAdmission(ctx, "a2", "owner-2", time.Minute, 5)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if result != ClaimAcquired {
		t.Fatalf("reclaim = %v, want ClaimAcquired", result)
	}
	row, _ := store.GetAdmission(ctx, "a2")
	if row.LeaseOwner != "owner-2" || row.ClaimCount != 2 {
		t.Fatalf("row = %+v, want owner-2 with claim_count 2", row)
	}
}

func TestClaimAdmissionHonoursSchedule(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour).UnixMilli()
	pendingAdmission(t, store, "a3", future)

	result, err := store.ClaimAdmission(ctx, "a3", "owner-1", time.Minute, 5)
	if err != nil {
		t.Fatalf("claim future admission: %v", err)
	}
	if result != ClaimNotAcquired {
		t.Fatalf("claim of a future admission = %v, want ClaimNotAcquired", result)
	}

	pending, err := store.ListPendingAdmissions(ctx, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %d, want 0 before the scheduled time", len(pending))
	}
}

func TestFinishAdmissionIsFencedOnOwner(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pendingAdmission(t, store, "a4", 0)

	if _, err := store.ClaimAdmission(ctx, "a4", "owner-1", time.Minute, 5); err != nil {
		t.Fatalf("claim: %v", err)
	}

	ok, err := store.FinishAdmission(ctx, "a4", "owner-2", model.RunStateSucceeded, "")
	if err != nil {
		t.Fatalf("stale finish: %v", err)
	}
	if ok {
		t.Fatalf("a stale owner must not record an outcome")
	}

	ok, err = store.FinishAdmission(ctx, "a4", "owner-1", model.RunStateSucceeded, "")
	if err != nil {
		t.Fatalf("owner finish: %v", err)
	}
	if !ok {
		t.Fatalf("the current owner must be able to record an outcome")
	}
	row, _ := store.GetAdmission(ctx, "a4")
	if row.State != model.RunStateSucceeded || row.LeaseOwner != "" || row.LeaseUntil != 0 {
		t.Fatalf("row = %+v, want succeeded with the lease cleared", row)
	}
}

func TestCancelAdmissionWinsOverOwner(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pendingAdmission(t, store, "a5", 0)

	if _, err := store.ClaimAdmission(ctx, "a5", "owner-1", time.Minute, 5); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.CancelAdmission(ctx, "a5", "canceled by user"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	held, err := store.RenewAdmission(ctx, "a5", "owner-1", time.Minute)
	if err != nil {
		t.Fatalf("renew after cancel: %v", err)
	}
	if held {
		t.Fatalf("renewal must fail once the admission is canceled")
	}

	ok, err := store.FinishAdmission(ctx, "a5", "owner-1", model.RunStateSucceeded, "")
	if err != nil {
		t.Fatalf("finish after cancel: %v", err)
	}
	if ok {
		t.Fatalf("a canceled admission must not be rewritten as succeeded")
	}
	row, _ := store.GetAdmission(ctx, "a5")
	if row.State != model.RunStateCanceled {
		t.Fatalf("state = %q, want canceled", row.State)
	}
}

// TestExhaustionNeverFailsALiveRun pins the boundary between "at the limit"
// and "exhausted": a row whose last claim is still running under a live lease
// belongs to its owner, and a competing dispatcher must neither report it as
// exhausted nor fail it underneath the owner.
func TestExhaustionNeverFailsALiveRun(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pendingAdmission(t, store, "a7", 0)

	if _, err := store.ClaimAdmission(ctx, "a7", "owner-1", time.Minute, 1); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	result, err := store.ClaimAdmission(ctx, "a7", "owner-2", time.Minute, 1)
	if err != nil {
		t.Fatalf("competing claim: %v", err)
	}
	if result != ClaimNotAcquired {
		t.Fatalf("competing claim = %v, want ClaimNotAcquired for a live lease", result)
	}

	if err := store.ExhaustAdmission(ctx, "a7", 1); err != nil {
		t.Fatalf("exhaust: %v", err)
	}
	row, _ := store.GetAdmission(ctx, "a7")
	if row.State != model.RunStateRunning {
		t.Fatalf("state = %q, want running; a live owner must not be failed", row.State)
	}
}

func TestClaimAdmissionExhaustsAtLimit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pendingAdmission(t, store, "a6", 0)

	if _, err := store.ClaimAdmission(ctx, "a6", "owner-1", time.Minute, 1); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := store.db.Model(&model.RunAdmissionM{}).
		Where("id = ?", "a6").Update("lease_until", 0).Error; err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	result, err := store.ClaimAdmission(ctx, "a6", "owner-2", time.Minute, 1)
	if err != nil {
		t.Fatalf("claim at limit: %v", err)
	}
	if result != ClaimExhausted {
		t.Fatalf("claim = %v, want ClaimExhausted", result)
	}

	if err := store.ExhaustAdmission(ctx, "a6", 1); err != nil {
		t.Fatalf("exhaust: %v", err)
	}
	row, _ := store.GetAdmission(ctx, "a6")
	if row.State != model.RunStateFailed {
		t.Fatalf("state = %q, want failed", row.State)
	}
}

func TestListAdmissionPageFiltersPagesAndCounts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	rows := []*model.RunAdmissionM{
		{
			ID: "a1", DedupKey: "a1", SessionID: "s1", Subject: "u1", Agent: "ops",
			Trigger: "scheduled", Source: "scheduled-run", State: model.RunStateSucceeded,
			CreatedAt: now - 2000,
		},
		{
			ID: "a2", DedupKey: "a2", SessionID: "s2", Subject: "u2", Agent: "ops",
			Trigger: "manual", Source: "scheduled-run", State: model.RunStateFailed,
			CreatedAt: now - 1000,
		},
		{
			ID: "a3", DedupKey: "a3", SessionID: "s3", Subject: "u3", Agent: "other",
			Trigger: "api", Source: "api", State: model.RunStateRunning,
			CreatedAt: now,
		},
	}
	for _, row := range rows {
		if _, _, err := store.AdmitRun(ctx, row); err != nil {
			t.Fatalf("admit %s: %v", row.ID, err)
		}
	}

	total, page, err := store.ListAdmissionPage(ctx, AdmissionFilter{Agent: "ops", Limit: 1})
	if err != nil {
		t.Fatalf("list by agent: %v", err)
	}
	if total != 2 || len(page) != 1 || page[0].ID != "a2" {
		t.Fatalf("agent page = total %d, rows %+v; want total 2 and newest a2", total, page)
	}

	total, failed, err := store.ListAdmissionPage(ctx, AdmissionFilter{State: model.RunStateFailed})
	if err != nil {
		t.Fatalf("list by state: %v", err)
	}
	if total != 1 || len(failed) != 1 || failed[0].ID != "a2" {
		t.Fatalf("state page = total %d, rows %+v; want only a2", total, failed)
	}

	total, matched, err := store.ListAdmissionPage(ctx, AdmissionFilter{Keyword: "A1"})
	if err != nil {
		t.Fatalf("list by keyword: %v", err)
	}
	if total != 1 || len(matched) != 1 || matched[0].ID != "a1" {
		t.Fatalf("keyword page = total %d, rows %+v; want only a1", total, matched)
	}

	total, manual, err := store.ListAdmissionPage(ctx, AdmissionFilter{Trigger: "manual"})
	if err != nil {
		t.Fatalf("list by trigger: %v", err)
	}
	if total != 1 || manual[0].ID != "a2" {
		t.Fatalf("trigger page = total %d, rows %+v; want only a2", total, manual)
	}
}

func TestListRunsByIDsSkipsPending(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	pendingAdmission(t, store, "pending-1", 0)

	if err := store.CreateRun(ctx, &model.RunM{ID: "started-1", SessionID: "s1", State: model.RunStateRunning}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	runs, err := store.ListRunsByIDs(ctx, []string{"pending-1", "started-1", "missing"})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "started-1" {
		t.Fatalf("runs = %+v, want only started-1", runs)
	}

	empty, err := store.ListRunsByIDs(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty ids = %+v, %v; want no rows and no error", empty, err)
	}
}

func TestListAgentRunWindowJoinsFiltersAndCaps(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	admissions := []*model.RunAdmissionM{
		{
			ID: "ops-settled", DedupKey: "ops-settled", Agent: "ops", SessionID: "s1",
			Trigger: "manual", State: model.RunStateSucceeded, CreatedAt: now - 5_000,
		},
		{
			ID: "ops-pending", DedupKey: "ops-pending", Agent: "ops", SessionID: "s2",
			Trigger: "scheduled", State: model.RunStatePending, CreatedAt: now - 1_000,
		},
		{
			ID: "other-settled", DedupKey: "other-settled", Agent: "other", SessionID: "s3",
			Trigger: "manual", State: model.RunStateFailed, CreatedAt: now - 2_000,
		},
		{
			ID: "ops-outside", DedupKey: "ops-outside", Agent: "ops", SessionID: "s4",
			Trigger: "manual", State: model.RunStateFailed, CreatedAt: now - 60_000,
		},
	}
	for _, admission := range admissions {
		if err := store.db.Create(admission).Error; err != nil {
			t.Fatalf("create admission %s: %v", admission.ID, err)
		}
	}

	settled := &model.RunM{
		ID: "ops-settled", SessionID: "s1", State: model.RunStateCompleted,
		UsageJSON: `{"Calls":2,"PromptTokens":10,"CompletionTokens":4,"TotalTokens":14}`,
		StartedAt: now - 4_800, FinishedAt: now - 4_000,
	}
	if err := store.CreateRun(ctx, settled); err != nil {
		t.Fatalf("create run: %v", err)
	}

	rows, err := store.ListAgentRunWindow(ctx, AgentRunWindowFilter{
		Agent: "ops", From: now - 10_000, To: now, ScanLimit: 10,
	})
	if err != nil {
		t.Fatalf("list window: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (the other agent and the out-of-window row are excluded)", len(rows))
	}
	if rows[0].ID != "ops-pending" || rows[1].ID != "ops-settled" {
		t.Fatalf("rows = [%s %s], want newest first", rows[0].ID, rows[1].ID)
	}
	if rows[0].RunState != "" || rows[0].StartedAt != 0 || rows[0].FinishedAt != 0 {
		t.Fatalf("pending row = %+v, want an empty run half", rows[0])
	}
	if rows[1].RunState != model.RunStateCompleted || rows[1].UsageJSON == "" ||
		rows[1].StartedAt != now-4_800 || rows[1].FinishedAt != now-4_000 {
		t.Fatalf("settled row = %+v, want the joined run fields", rows[1])
	}

	capped, err := store.ListAgentRunWindow(ctx, AgentRunWindowFilter{
		Agent: "ops", From: now - 10_000, To: now, ScanLimit: 1,
	})
	if err != nil {
		t.Fatalf("capped list: %v", err)
	}
	if len(capped) != 1 || capped[0].ID != "ops-pending" {
		t.Fatalf("capped = %+v, want only the newest row", capped)
	}
}

func TestListAgentRunWindowFleetAndPlaceholderParity(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	admissions := []*model.RunAdmissionM{
		{
			ID: "ops-run", DedupKey: "ops-run", Agent: "ops", SessionID: "s1",
			Trigger: "manual", State: model.RunStateSucceeded, CreatedAt: now - 5_000,
		},
		{
			ID: "other-run", DedupKey: "other-run", Agent: "other", SessionID: "s2",
			Trigger: "scheduled", State: model.RunStateFailed, CreatedAt: now - 3_000,
		},
	}
	for _, admission := range admissions {
		if err := store.db.Create(admission).Error; err != nil {
			t.Fatalf("create admission %s: %v", admission.ID, err)
		}
	}
	if err := store.db.Create(&model.SessionM{
		ID: "s-chat", Agent: "ops", Owner: "u1", State: model.SessionStateReady,
	}).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.CreateRun(ctx, &model.RunM{
		ID: "chat-run", SessionID: "s-chat", State: model.RunStateCompleted,
		StartedAt: now - 1_000, FinishedAt: now - 500,
	}); err != nil {
		t.Fatalf("create interactive run: %v", err)
	}

	rows, err := store.ListAgentRunWindow(ctx, AgentRunWindowFilter{
		From: now - 10_000, To: now, ScanLimit: 10,
	})
	if err != nil {
		t.Fatalf("fleet list window: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want all three agents' rows", len(rows))
	}
	byID := map[string]MonitorRunRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if byID["ops-run"].Agent != "ops" || byID["ops-run"].Source != "admission" {
		t.Fatalf("admission row = %+v", byID["ops-run"])
	}
	if byID["other-run"].Agent != "other" || byID["other-run"].Source != "admission" {
		t.Fatalf("other row = %+v", byID["other-run"])
	}
	if byID["chat-run"].Agent != "ops" || byID["chat-run"].Source != "run" {
		t.Fatalf("interactive row = %+v", byID["chat-run"])
	}

	column := func(alias, name string) string { return quoteIdentifier(store.db, alias+"."+name) }
	for _, filter := range []AgentRunWindowFilter{
		{Agent: "ops", From: 1, To: 2},
		{From: 1, To: 2},
	} {
		admissionSQL, admissionArgs := buildAdmissionWindowWhere(column, filter)
		if strings.Count(admissionSQL, "?") != len(admissionArgs) {
			t.Fatalf("admission where = %q with %d args", admissionSQL, len(admissionArgs))
		}
		interactiveSQL, interactiveArgs := buildInteractiveWindowWhere(column, filter)
		if strings.Count(interactiveSQL, "?") != len(interactiveArgs) {
			t.Fatalf("interactive where = %q with %d args", interactiveSQL, len(interactiveArgs))
		}
	}
}

func TestListStuckRuns(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	admissions := []*model.RunAdmissionM{
		// Due, never claimed: stuck.
		{ID: "pending-stuck", DedupKey: "pending-stuck", Agent: "ops", SessionID: "s1",
			State: model.RunStatePending, CreatedAt: now - 20*60_000},
		// Scheduled in the future: not stuck even though it is old.
		{ID: "pending-future", DedupKey: "pending-future", Agent: "ops", SessionID: "s2",
			State: model.RunStatePending, ScheduledTime: now + 3_600_000, CreatedAt: now - 20*60_000},
		// Running with a live lease and inside the running threshold: not stuck.
		{ID: "running-healthy", DedupKey: "running-healthy", Agent: "ops", SessionID: "s3",
			State: model.RunStateRunning, LeaseUntil: now + 60_000, CreatedAt: now - 30*60_000},
		// Running past the threshold: stuck.
		{ID: "running-timeout", DedupKey: "running-timeout", Agent: "ops", SessionID: "s4",
			State: model.RunStateRunning, LeaseUntil: now + 60_000, CreatedAt: now - 90*60_000},
		// Running with an expired lease: stuck regardless of age.
		{ID: "running-lease", DedupKey: "running-lease", Agent: "ops", SessionID: "s5",
			State: model.RunStateRunning, LeaseUntil: now - 60_000, CreatedAt: now - 5*60_000},
	}
	for _, admission := range admissions {
		if err := store.db.Create(admission).Error; err != nil {
			t.Fatalf("create admission %s: %v", admission.ID, err)
		}
	}
	if err := store.db.Create(&model.SessionM{
		ID: "s-chat", Agent: "chat", Owner: "u1", State: model.SessionStateReady,
	}).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.CreateRun(ctx, &model.RunM{
		ID: "chat-stuck", SessionID: "s-chat", State: model.RunStateRunning,
		StartedAt: now - 90*60_000,
	}); err != nil {
		t.Fatalf("create interactive run: %v", err)
	}

	rows, err := store.ListStuckRuns(ctx, StuckRunFilter{
		Now:           now,
		PendingBefore: now - 10*60_000,
		RunningBefore: now - 60*60_000,
		Limit:         50,
	})
	if err != nil {
		t.Fatalf("list stuck: %v", err)
	}

	got := map[string]StuckRunRow{}
	for _, row := range rows {
		got[row.ID] = row
	}
	for _, id := range []string{"pending-stuck", "running-timeout", "running-lease", "chat-stuck"} {
		if _, ok := got[id]; !ok {
			t.Fatalf("stuck rows = %+v, missing %s", rows, id)
		}
	}
	for _, id := range []string{"pending-future", "running-healthy"} {
		if _, ok := got[id]; ok {
			t.Fatalf("stuck rows = %+v, %s must not be stuck", rows, id)
		}
	}
	if got["chat-stuck"].Source != "run" || got["chat-stuck"].Agent != "chat" {
		t.Fatalf("interactive stuck row = %+v", got["chat-stuck"])
	}
	if got["running-lease"].LeaseUntil != now-60_000 {
		t.Fatalf("lease field = %+v", got["running-lease"])
	}
}
