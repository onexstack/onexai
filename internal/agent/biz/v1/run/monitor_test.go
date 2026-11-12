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

package run

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func newMonitorBiz(t *testing.T) (*runBiz, *runstore.SQLStore, *gorm.DB) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		if strings.Contains(err.Error(), "requires cgo") || strings.Contains(err.Error(), "stub") {
			t.Skipf("sqlite requires cgo: %v", err)
		}
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.AutoMigrate(&model.RunAdmissionM{}, &model.RunM{}, &model.SessionM{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := runstore.New(db)
	return New(store, nil), store, db
}

func TestListAdminRunMonitorAggregates(t *testing.T) {
	biz, store, db := newMonitorBiz(t)
	ctx := context.Background()
	base := int64(1_760_000_000_000)

	admissions := []*model.RunAdmissionM{
		{ID: "a1", DedupKey: "a1", Agent: "ops", SessionID: "s1", Trigger: "manual",
			State: model.RunStateSucceeded, CreatedAt: base + 30_000},
		{ID: "a2", DedupKey: "a2", Agent: "ops", SessionID: "s2", Trigger: "scheduled",
			State: model.RunStateFailed, CreatedAt: base + 90_000},
		{ID: "a3", DedupKey: "a3", Agent: "ops", SessionID: "s3", Trigger: "manual",
			State: model.RunStateCanceled, CreatedAt: base + 200_000},
		{ID: "a4", DedupKey: "a4", Agent: "ops", SessionID: "s4", Trigger: "scheduled",
			State: model.RunStatePending, CreatedAt: base + 3_500_000},
		// Outside the window and owned by another agent: both must be ignored.
		{ID: "a5", DedupKey: "a5", Agent: "ops", SessionID: "s5", Trigger: "manual",
			State: model.RunStateFailed, CreatedAt: base - 1},
		{ID: "a6", DedupKey: "a6", Agent: "other", SessionID: "s6", Trigger: "manual",
			State: model.RunStateSucceeded, CreatedAt: base + 10_000},
	}
	for _, admission := range admissions {
		if err := db.Create(admission).Error; err != nil {
			t.Fatalf("create admission %s: %v", admission.ID, err)
		}
	}

	runs := []*model.RunM{
		{ID: "a1", SessionID: "s1", State: model.RunStateCompleted,
			UsageJSON: `{"Calls":2,"PromptTokens":100,"CompletionTokens":50,"TotalTokens":150,"CacheHitTokens":30,"CacheMissTokens":20}`,
			StartedAt: base + 31_000, FinishedAt: base + 33_000},
		{ID: "a2", SessionID: "s2", State: model.RunStateFailed,
			StartedAt: base + 91_000, FinishedAt: base + 101_000},
		{ID: "a3", SessionID: "s3", State: model.RunStateCanceled,
			StartedAt: base + 201_000, FinishedAt: base + 206_000},
	}
	for _, row := range runs {
		if err := store.CreateRun(ctx, row); err != nil {
			t.Fatalf("create run %s: %v", row.ID, err)
		}
	}

	response, err := biz.ListAdminRunMonitor(ctx, &v1.ListAdminRunMonitorQuery{
		Agent: "ops", From: base, To: base + 3_600_000, Step: v1.MonitorStep1m, Limit: 2,
	})
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}

	if response.Agent != "ops" || response.Step != v1.MonitorStep1m || response.Truncated {
		t.Fatalf("response envelope = %+v", response)
	}
	if len(response.Buckets) != 60 {
		t.Fatalf("buckets = %d, want 60", len(response.Buckets))
	}

	summary := response.Summary
	if summary.Total != 4 || summary.Succeeded != 1 || summary.Failed != 1 ||
		summary.Canceled != 1 || summary.Active != 1 {
		t.Fatalf("summary counts = %+v", summary)
	}
	if summary.SuccessRate != 0.3333 {
		t.Fatalf("success rate = %v, want 0.3333", summary.SuccessRate)
	}
	if summary.Duration.Samples != 3 || summary.Duration.AvgMs != 5_666 ||
		summary.Duration.P95Ms != 10_000 || summary.Duration.MaxMs != 10_000 {
		t.Fatalf("duration = %+v", summary.Duration)
	}
	if summary.Tokens.PromptTokens != 100 || summary.Tokens.CompletionTokens != 50 ||
		summary.Tokens.TotalTokens != 150 || summary.Tokens.Calls != 2 ||
		summary.Tokens.RunsWithUsage != 1 || summary.Tokens.CacheHitRate != 0.6 {
		t.Fatalf("tokens = %+v", summary.Tokens)
	}

	if response.Buckets[0].Succeeded != 1 || response.Buckets[0].DurationSamples != 1 {
		t.Fatalf("first bucket = %+v", response.Buckets[0])
	}
	if response.Buckets[1].Failed != 1 || response.Buckets[3].Canceled != 1 ||
		response.Buckets[58].Active != 1 {
		t.Fatalf("bucket placement = %+v", response.Buckets)
	}

	triggers := map[string]int{}
	for _, item := range response.Triggers {
		triggers[item.Trigger] = item.Count
	}
	if triggers["manual"] != 2 || triggers["scheduled"] != 2 {
		t.Fatalf("triggers = %+v", response.Triggers)
	}

	distribution := map[string]int{}
	for _, item := range response.DurationDistribution {
		distribution[item.Key] = item.Count
	}
	if distribution["1sTo5s"] != 1 || distribution["5sTo30s"] != 2 || distribution["lt1s"] != 0 {
		t.Fatalf("duration distribution = %+v", response.DurationDistribution)
	}

	if len(response.TopSlowRuns) != 3 ||
		response.TopSlowRuns[0].ID != "a2" || response.TopSlowRuns[1].ID != "a3" ||
		response.TopSlowRuns[2].ID != "a1" {
		t.Fatalf("top slow = %+v", response.TopSlowRuns)
	}

	if len(response.RecentRuns) != 2 ||
		response.RecentRuns[0].ID != "a4" || response.RecentRuns[1].ID != "a3" {
		t.Fatalf("recent = %+v", response.RecentRuns)
	}
}

func TestListAdminRunMonitorEmptyWindow(t *testing.T) {
	biz, _, _ := newMonitorBiz(t)
	base := time.Now().UnixMilli()

	response, err := biz.ListAdminRunMonitor(context.Background(), &v1.ListAdminRunMonitorQuery{
		Agent: "missing", From: base - 3_600_000, To: base, Step: v1.MonitorStep1m, Limit: 10,
	})
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if response.Summary.Total != 0 || response.Summary.Duration.Samples != 0 ||
		response.Summary.Tokens.RunsWithUsage != 0 {
		t.Fatalf("summary = %+v, want zeros", response.Summary)
	}
	if response.Buckets == nil || len(response.Buckets) != 60 {
		t.Fatalf("buckets = %+v, want 60 empty buckets", response.Buckets)
	}
	if response.Triggers == nil || response.TopSlowRuns == nil || response.RecentRuns == nil {
		t.Fatal("empty arrays must serialize as [] rather than null")
	}
}

func TestListAdminRunMonitorPendingOnly(t *testing.T) {
	biz, _, db := newMonitorBiz(t)
	ctx := context.Background()
	base := int64(1_760_000_000_000)

	if err := db.Create(&model.RunAdmissionM{
		ID: "pending", DedupKey: "pending", Agent: "ops", SessionID: "s",
		Trigger: "manual", State: model.RunStatePending, CreatedAt: base + 1_000,
	}).Error; err != nil {
		t.Fatalf("create admission: %v", err)
	}

	response, err := biz.ListAdminRunMonitor(ctx, &v1.ListAdminRunMonitorQuery{
		Agent: "ops", From: base, To: base + 3_600_000, Step: v1.MonitorStep1m, Limit: 10,
	})
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if response.Summary.Total != 1 || response.Summary.Active != 1 ||
		response.Summary.Duration.Samples != 0 || response.Summary.Tokens.RunsWithUsage != 0 {
		t.Fatalf("summary = %+v", response.Summary)
	}
	if len(response.RecentRuns) != 1 || response.RecentRuns[0].DurationMs != 0 {
		t.Fatalf("recent = %+v", response.RecentRuns)
	}
}

// Interactive runs are recorded straight into the run table by the engine; they
// never pass through an admission. The monitor has to read them through the
// owning session, and it must not count an admitted run twice.
func TestListAdminRunMonitorIncludesInteractiveRuns(t *testing.T) {
	biz, store, db := newMonitorBiz(t)
	ctx := context.Background()
	base := int64(1_760_000_000_000)

	// An asynchronous run: admission plus its joined run row.
	if err := db.Create(&model.RunAdmissionM{
		ID: "scheduled-run", DedupKey: "scheduled-run", Agent: "ops", SessionID: "s-async",
		Trigger: "scheduled", State: model.RunStateSucceeded, CreatedAt: base + 1_000,
	}).Error; err != nil {
		t.Fatalf("create admission: %v", err)
	}
	if err := store.CreateRun(ctx, &model.RunM{
		ID: "scheduled-run", SessionID: "s-async", State: model.RunStateCompleted,
		StartedAt: base + 1_500, FinishedAt: base + 2_500,
	}); err != nil {
		t.Fatalf("create admitted run: %v", err)
	}

	// A console/chat turn for the same agent: session plus run, no admission.
	if err := db.Create(&model.SessionM{
		ID: "dbg:ops:u1", Agent: "ops", Owner: "u1", State: model.SessionStateReady,
	}).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.CreateRun(ctx, &model.RunM{
		ID: "chat-run", SessionID: "dbg:ops:u1", State: model.RunStateCompleted,
		UsageJSON: `{"Calls":1,"PromptTokens":7,"CompletionTokens":3,"TotalTokens":10}`,
		StartedAt: base + 10_000, FinishedAt: base + 12_000,
	}); err != nil {
		t.Fatalf("create interactive run: %v", err)
	}

	// A turn of another agent's session must stay out of this agent's window.
	if err := db.Create(&model.SessionM{
		ID: "dbg:other:u1", Agent: "other", Owner: "u1", State: model.SessionStateReady,
	}).Error; err != nil {
		t.Fatalf("create other session: %v", err)
	}
	if err := store.CreateRun(ctx, &model.RunM{
		ID: "other-run", SessionID: "dbg:other:u1", State: model.RunStateCompleted,
		StartedAt: base + 11_000, FinishedAt: base + 11_500,
	}); err != nil {
		t.Fatalf("create other run: %v", err)
	}

	response, err := biz.ListAdminRunMonitor(ctx, &v1.ListAdminRunMonitorQuery{
		Agent: "ops", From: base, To: base + 3_600_000, Step: v1.MonitorStep1m, Limit: 10,
	})
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}

	// The admitted run appears once (through its admission), and the interactive
	// run is counted as a manual run.
	if response.Summary.Total != 2 || response.Summary.Succeeded != 2 {
		t.Fatalf("summary = %+v, want two successes", response.Summary)
	}
	if response.Summary.Duration.Samples != 2 || response.Summary.Duration.AvgMs != 1_500 {
		t.Fatalf("duration = %+v", response.Summary.Duration)
	}
	if response.Summary.Tokens.TotalTokens != 10 || response.Summary.Tokens.RunsWithUsage != 1 {
		t.Fatalf("tokens = %+v, want the interactive run's usage", response.Summary.Tokens)
	}

	triggers := map[string]int{}
	for _, item := range response.Triggers {
		triggers[item.Trigger] = item.Count
	}
	if triggers["scheduled"] != 1 || triggers["manual"] != 1 {
		t.Fatalf("triggers = %+v", response.Triggers)
	}

	if len(response.RecentRuns) != 2 ||
		response.RecentRuns[0].ID != "chat-run" || response.RecentRuns[1].ID != "scheduled-run" {
		t.Fatalf("recent = %+v, want newest first", response.RecentRuns)
	}
	if response.RecentRuns[0].CreatedAt != base+10_000 {
		t.Fatalf("interactive run window anchor = %d, want startedAt %d",
			response.RecentRuns[0].CreatedAt, base+10_000)
	}
}

// The fleet view (all=true) must aggregate every agent, including interactive
// runs that never passed through an admission, and carry the agent/source of
// each recent run.
func TestListAdminRunMonitorFleetAggregates(t *testing.T) {
	biz, store, db := newMonitorBiz(t)
	ctx := context.Background()
	base := int64(1_760_000_000_000)

	for _, admission := range []*model.RunAdmissionM{
		{ID: "a1", DedupKey: "a1", Agent: "ops", SessionID: "s1", Trigger: "manual",
			State: model.RunStateSucceeded, CreatedAt: base + 1_000},
		{ID: "a2", DedupKey: "a2", Agent: "other", SessionID: "s2", Trigger: "scheduled",
			State: model.RunStateFailed, CreatedAt: base + 2_000},
	} {
		if err := db.Create(admission).Error; err != nil {
			t.Fatalf("create admission %s: %v", admission.ID, err)
		}
	}
	for _, row := range []*model.RunM{
		{ID: "a1", SessionID: "s1", State: model.RunStateCompleted,
			UsageJSON: `{"Calls":1,"PromptTokens":10,"CompletionTokens":5,"TotalTokens":15}`,
			StartedAt: base + 1_500, FinishedAt: base + 2_500},
		{ID: "a2", SessionID: "s2", State: model.RunStateFailed,
			StartedAt: base + 2_500, FinishedAt: base + 3_500},
	} {
		if err := store.CreateRun(ctx, row); err != nil {
			t.Fatalf("create run %s: %v", row.ID, err)
		}
	}
	if err := db.Create(&model.SessionM{
		ID: "s-chat", Agent: "ops", Owner: "u1", State: model.SessionStateReady,
	}).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.CreateRun(ctx, &model.RunM{
		ID: "chat-run", SessionID: "s-chat", State: model.RunStateCompleted,
		UsageJSON: `{"Calls":1,"PromptTokens":7,"CompletionTokens":3,"TotalTokens":10}`,
		StartedAt: base + 5_000, FinishedAt: base + 6_000,
	}); err != nil {
		t.Fatalf("create interactive run: %v", err)
	}

	response, err := biz.ListAdminRunMonitor(ctx, &v1.ListAdminRunMonitorQuery{
		All: true, From: base, To: base + 3_600_000, Step: v1.MonitorStep1m, Limit: 10,
	})
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}

	if response.Agent != "" || !response.All || response.ScannedRows != 3 {
		t.Fatalf("envelope = %+v", response)
	}
	if response.Summary.Total != 3 || response.Summary.Succeeded != 2 ||
		response.Summary.Failed != 1 || response.Summary.Active != 0 {
		t.Fatalf("summary counts = %+v", response.Summary)
	}

	agents := map[string]AgentMonitorSummary{}
	for _, item := range response.Agents {
		agents[item.Agent] = item
	}
	if len(agents) != 2 {
		t.Fatalf("agents = %+v, want ops and other", response.Agents)
	}
	if ops := agents["ops"]; ops.Total != 2 || ops.Succeeded != 2 ||
		ops.Tokens.TotalTokens != 25 || ops.Duration.Samples != 2 {
		t.Fatalf("ops summary = %+v", ops)
	}
	if other := agents["other"]; other.Total != 1 || other.Failed != 1 ||
		other.SuccessRate != 0 || other.LastRunAt != base+2_000 {
		t.Fatalf("other summary = %+v", other)
	}

	if len(response.RecentRuns) != 3 ||
		response.RecentRuns[0].ID != "chat-run" || response.RecentRuns[0].Source != "run" ||
		response.RecentRuns[0].Agent != "ops" || response.RecentRuns[0].TotalTokens != 10 {
		t.Fatalf("recent = %+v", response.RecentRuns)
	}
	if response.RecentRuns[1].ID != "a2" || response.RecentRuns[1].Source != "admission" ||
		response.RecentRuns[1].Agent != "other" {
		t.Fatalf("admission recent = %+v", response.RecentRuns[1])
	}
}

func TestPercentileNearestRank(t *testing.T) {
	sorted := []int64{10, 20, 30, 40}
	if got := percentileNearestRank(sorted, 95); got != 40 {
		t.Fatalf("p95 = %d, want 40", got)
	}
	if got := percentileNearestRank(sorted, 50); got != 20 {
		t.Fatalf("p50 = %d, want 20", got)
	}
	if got := percentileNearestRank(nil, 95); got != 0 {
		t.Fatalf("empty p95 = %d, want 0", got)
	}
}
