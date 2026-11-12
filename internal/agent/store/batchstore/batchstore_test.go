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

package batchstore

import (
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/onexstack/onexai/internal/agent/model"
	batchv1beta1 "github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
)

func TestScheduledRunRoundTrip(t *testing.T) {
	row := &model.ScheduledRunM{
		Name:                  "daily",
		Namespace:             "default",
		Agent:                 "ops-agent",
		Schedule:              "0 9 * * *",
		TimeZone:              "Asia/Shanghai",
		Prompt:                "summarize yesterday",
		Owner:                 "ou_1",
		Paused:                true,
		ExecutionTimeoutNanos: int64(15 * 60 * 1e9),
		Labels:                model.StringMap{"team": "sre"},
	}

	cronJob, err := cronJobFromRow(row, "default")
	if err != nil {
		t.Fatalf("cronJobFromRow: %v", err)
	}
	if cronJob.Spec.Schedule != row.Schedule {
		t.Fatalf("schedule = %q", cronJob.Spec.Schedule)
	}
	if cronJob.Spec.TimeZone == nil || *cronJob.Spec.TimeZone != row.TimeZone {
		t.Fatalf("timeZone not preserved: %+v", cronJob.Spec.TimeZone)
	}
	if cronJob.Spec.Suspend == nil || !*cronJob.Spec.Suspend {
		t.Fatal("paused schedule must be suspended")
	}
	if cronJob.Spec.JobTemplate.Spec.Type != batchv1beta1.JobTypeAgentRun {
		t.Fatalf("job type = %q", cronJob.Spec.JobTemplate.Spec.Type)
	}
	if cronJob.Spec.JobTemplate.Spec.ActiveDeadlineSeconds == nil ||
		*cronJob.Spec.JobTemplate.Spec.ActiveDeadlineSeconds != 900 {
		t.Fatalf("activeDeadlineSeconds not preserved: %+v", cronJob.Spec.JobTemplate.Spec.ActiveDeadlineSeconds)
	}

	got := rowFromCronJob(cronJob)
	if got.Agent != row.Agent || got.Prompt != row.Prompt || got.Owner != row.Owner {
		t.Fatalf("provider spec not round-tripped: %+v", got)
	}
	if got.Schedule != row.Schedule || got.TimeZone != row.TimeZone {
		t.Fatalf("schedule not round-tripped: %+v", got)
	}
	if !got.Paused {
		t.Fatal("paused flag not round-tripped")
	}
	if got.ExecutionTimeoutNanos != row.ExecutionTimeoutNanos {
		t.Fatalf("timeout = %d, want %d", got.ExecutionTimeoutNanos, row.ExecutionTimeoutNanos)
	}
	if got.Status != model.StatusDisabled {
		t.Fatalf("status = %q, want %q", got.Status, model.StatusDisabled)
	}
}

func TestIsScheduledRun(t *testing.T) {
	owned := &batchv1beta1.CronJob{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{ManagedByAnnotation: ManagedByScheduled},
	}}
	if !isScheduledRun(owned) {
		t.Fatal("managed-by annotation must be recognized")
	}
	migrated := &batchv1beta1.CronJob{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{MigratedFromAnnotation: "ScheduledRun/ns/name"},
	}}
	if !isScheduledRun(migrated) {
		t.Fatal("migrated-from annotation must be recognized")
	}
	foreign := &batchv1beta1.CronJob{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{"other": "x"},
	}}
	if isScheduledRun(foreign) {
		t.Fatal("a CronJob owned by another caller must be ignored")
	}
}

func TestExecutionFromJob(t *testing.T) {
	spec, _ := json.Marshal(batchv1beta1.AgentRunProviderSpec{Agent: "a", Prompt: "p"})
	status, _ := json.Marshal(batchv1beta1.AgentRunProviderStatus{
		RunID: "run-1", SessionID: "sess-1", Phase: "succeeded",
	})
	started := metav1.NewTime(metav1.Now().Time)
	ended := metav1.NewTime(started.Add(1_000_000_000))

	job := &batchv1beta1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "daily-1",
			Namespace:         "default",
			CreationTimestamp: started,
			Annotations: map[string]string{
				ScheduleLabel:     "daily",
				TriggerAnnotation: "manual",
			},
		},
		Spec: batchv1beta1.JobSpec{
			Type:         batchv1beta1.JobTypeAgentRun,
			ProviderSpec: &runtime.RawExtension{Raw: spec},
		},
		Status: batchv1beta1.JobStatus{
			Phase:          batchv1beta1.JobPhaseSucceeded,
			StartedAt:      &started,
			EndedAt:        &ended,
			ProviderStatus: &runtime.RawExtension{Raw: status},
			Conditions: []batchv1beta1.JobCondition{{
				Type: batchv1beta1.JobComplete, Status: corev1.ConditionTrue,
			}},
		},
	}

	execution := executionFromJob(job)
	if execution.ID != "daily-1" || execution.Schedule != "daily" {
		t.Fatalf("identity not mapped: %+v", execution)
	}
	if execution.Trigger != "manual" {
		t.Fatalf("trigger = %q", execution.Trigger)
	}
	if execution.State != model.ExecutionStateCompleted {
		t.Fatalf("state = %q", execution.State)
	}
	if execution.Prompt != "p" || execution.RunID != "run-1" || execution.SessionID != "sess-1" {
		t.Fatalf("payload not mapped: %+v", execution)
	}
	if execution.FinishedAt <= execution.StartedAt {
		t.Fatalf("timestamps not mapped: %+v", execution)
	}
}

func TestExecutionStateFallsBackToPhase(t *testing.T) {
	job := &batchv1beta1.Job{Status: batchv1beta1.JobStatus{Phase: batchv1beta1.JobPhaseFailed}}
	if got := stateOf(job); got != model.ExecutionStateFailed {
		t.Fatalf("state = %q, want failed", got)
	}
	job.Status.Phase = batchv1beta1.JobPhaseRunning
	if got := stateOf(job); got != model.ExecutionStateRunning {
		t.Fatalf("state = %q, want running", got)
	}
}

func TestExecutionStatePendingBeforeStart(t *testing.T) {
	// A Job the controller has not started has no phase at all; showing that
	// as running would hide a controller that never dispatched it.
	for _, phase := range []batchv1beta1.JobPhase{"", batchv1beta1.JobPhasePending} {
		job := &batchv1beta1.Job{Status: batchv1beta1.JobStatus{Phase: phase}}
		if got := stateOf(job); got != model.ExecutionStatePending {
			t.Fatalf("phase %q: state = %q, want pending", phase, got)
		}
	}
}

func TestNextScheduledAtHonoursTimeZoneAndSuspend(t *testing.T) {
	// 2026-11-07 00:00 UTC is 08:00 in Shanghai, so 09:00 is the next run.
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	cronJob := &batchv1beta1.CronJob{
		Spec: batchv1beta1.CronJobSpec{
			Schedule: "0 9 * * *",
			TimeZone: ptr.To("Asia/Shanghai"),
		},
	}

	got := nextScheduledAt(cronJob, now)
	want := time.Date(2026, 11, 7, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60)).UnixMilli()
	if got != want {
		t.Fatalf("Asia/Shanghai next = %d, want %d", got, want)
	}

	cronJob.Spec.TimeZone = ptr.To("UTC")
	got = nextScheduledAt(cronJob, now)
	want = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC).UnixMilli()
	if got != want {
		t.Fatalf("UTC next = %d, want %d", got, want)
	}

	cronJob.Spec.Suspend = ptr.To(true)
	if got := nextScheduledAt(cronJob, now); got != 0 {
		t.Fatalf("paused next = %d, want 0", got)
	}

	cronJob.Spec.Suspend = nil
	cronJob.Spec.Schedule = "not a cron"
	if got := nextScheduledAt(cronJob, now); got != 0 {
		t.Fatalf("invalid cron next = %d, want 0", got)
	}
}
