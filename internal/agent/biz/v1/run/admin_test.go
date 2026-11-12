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

func TestAdminRunOfFallsBackToTheRequestPayload(t *testing.T) {
	payload := decodeAdmitRequest(`{
		"agent": "ops-agent",
		"prompt": "summarize yesterday",
		"subjectID": "ou_1",
		"trigger": "manual",
		"scheduleRef": "daily-report"
	}`)
	admission := &model.RunAdmissionM{
		ID:        "run-1",
		SessionID: "sess-1",
		Subject:   "ou_1",
		State:     model.RunStateFailed,
		Message:   "model endpoint unavailable",
		CreatedAt: 123,
	}

	got := adminRunOf(admission, nil, payload)
	if got.Agent != "ops-agent" {
		t.Fatalf("agent = %q, want ops-agent", got.Agent)
	}
	if got.Trigger != "manual" {
		t.Fatalf("trigger = %q, want manual", got.Trigger)
	}
	if got.ScheduleRef != "daily-report" {
		t.Fatalf("scheduleRef = %q, want daily-report", got.ScheduleRef)
	}
	if got.State != model.RunStateFailed {
		t.Fatalf("state = %q, want failed", got.State)
	}
	if got.Error != "model endpoint unavailable" {
		t.Fatalf("error = %q, want the admission message", got.Error)
	}
	if got.PromptPreview != "summarize yesterday" {
		t.Fatalf("promptPreview = %q", got.PromptPreview)
	}
	if got.SessionID != "sess-1" || got.CreatedAt != 123 {
		t.Fatalf("identity/timestamps not preserved: %+v", got)
	}
}

func TestAdminRunOfPrefersRunTimestampsAndError(t *testing.T) {
	admission := &model.RunAdmissionM{
		ID: "run-1", State: model.RunStateRunning, Message: "admission note",
	}
	row := &model.RunM{
		ID: "run-1", State: model.RunStateCompleted, Error: "run failed",
		StartedAt: 100, FinishedAt: 200,
	}

	got := adminRunOf(admission, row, &AdmitRequest{})
	if got.StartedAt != 100 || got.FinishedAt != 200 {
		t.Fatalf("timestamps = %d/%d, want 100/200", got.StartedAt, got.FinishedAt)
	}
	if got.Error != "run failed" {
		t.Fatalf("error = %q, want the run error", got.Error)
	}
	// The list state is the admission state, not the run's completed spelling.
	if got.State != model.RunStateRunning {
		t.Fatalf("state = %q, want running", got.State)
	}
}

func TestDeriveTrigger(t *testing.T) {
	cases := []struct {
		name      string
		admission *model.RunAdmissionM
		payload   *AdmitRequest
		want      string
	}{
		{"column wins", &model.RunAdmissionM{Trigger: "retry"}, &AdmitRequest{Trigger: "manual"}, "retry"},
		{"payload next", &model.RunAdmissionM{}, &AdmitRequest{Trigger: "manual"}, "manual"},
		{"metadata next", &model.RunAdmissionM{}, &AdmitRequest{Metadata: map[string]string{"trigger": "scheduled"}}, "scheduled"},
		{"schedule ref", &model.RunAdmissionM{ScheduleRef: "daily"}, &AdmitRequest{}, "scheduled"},
		{"unknown", &model.RunAdmissionM{}, &AdmitRequest{}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveTrigger(tc.admission, tc.payload); got != tc.want {
				t.Fatalf("trigger = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeRunState(t *testing.T) {
	if got := normalizeRunState(model.RunStateCompleted); got != model.RunStateSucceeded {
		t.Fatalf("completed = %q, want succeeded", got)
	}
	if got := normalizeRunState(model.RunStateFailed); got != model.RunStateFailed {
		t.Fatalf("failed = %q, want failed", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("你好世界", 2); got != "你好" {
		t.Fatalf("truncate = %q, want 你好", got)
	}
	if got := truncateRunes("short", 10); got != "short" {
		t.Fatalf("short = %q, want unchanged", got)
	}
}

func TestRetryAdminRunGuardsStates(t *testing.T) {
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
	if err := db.AutoMigrate(&model.RunAdmissionM{}, &model.RunM{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store := runstore.New(db)
	biz := New(store, nil)
	ctx := context.Background()
	const payload = `{"agent":"ops","prompt":"hello","subjectID":"u1","source":"verification"}`

	for _, state := range []string{model.RunStatePending, model.RunStateRunning, model.RunStateSucceeded} {
		id := "run-" + state
		if _, _, err := store.AdmitRun(ctx, &model.RunAdmissionM{
			ID: id, DedupKey: id, SessionID: "s-" + id, Subject: "u1", Agent: "ops",
			State: state, RequestJSON: payload, CreatedAt: time.Now().UnixMilli(),
		}); err != nil {
			t.Fatalf("admit %s: %v", state, err)
		}
		if _, err := biz.RetryAdminRun(ctx, &v1.RetryAdminRunRequest{ID: id}); err == nil {
			t.Fatalf("%s must not be retryable", state)
		}
	}

	failedID := "run-failed"
	if _, _, err := store.AdmitRun(ctx, &model.RunAdmissionM{
		ID: failedID, DedupKey: failedID, SessionID: "s-failed", Subject: "u1", Agent: "ops",
		State: model.RunStateFailed, RequestJSON: payload, CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("admit failed: %v", err)
	}
	retried, err := biz.RetryAdminRun(ctx, &v1.RetryAdminRunRequest{ID: failedID})
	if err != nil {
		t.Fatalf("retry failed run: %v", err)
	}
	if retried.ID == failedID {
		t.Fatal("retry must create a new admission")
	}
	if retried.Trigger != "retry" || retried.Agent != "ops" {
		t.Fatalf("retried admission = %+v, want trigger retry and agent ops", retried)
	}
}
