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

package cronjob

import (
	"fmt"
	"testing"
	"time"

	"k8s.io/utils/ptr"

	"github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
)

func TestScheduleForHonoursTimeZone(t *testing.T) {
	cj := &v1beta1.CronJob{Spec: v1beta1.CronJobSpec{
		Schedule: "0 9 * * *",
		TimeZone: ptr.To("Asia/Shanghai"),
	}}
	schedule, err := scheduleFor(cj)
	if err != nil {
		t.Fatalf("scheduleFor: %v", err)
	}

	// 09:00 Shanghai is 01:00 UTC.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next := schedule.Next(base)
	want := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestScheduleForRejectsInvalidTimeZone(t *testing.T) {
	cj := &v1beta1.CronJob{Spec: v1beta1.CronJobSpec{
		Schedule: "0 9 * * *",
		TimeZone: ptr.To("Mars/Olympus"),
	}}
	if _, err := scheduleFor(cj); err == nil {
		t.Fatal("expected an invalid time zone to fail")
	}
}

func TestScheduleForSupportsFiveFields(t *testing.T) {
	cj := &v1beta1.CronJob{Spec: v1beta1.CronJobSpec{Schedule: "*/5 * * * *"}}
	schedule, err := scheduleFor(cj)
	if err != nil {
		t.Fatalf("scheduleFor: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := schedule.Next(base); !got.Equal(base.Add(5 * time.Minute)) {
		t.Fatalf("next = %s", got)
	}
}

func TestJobFinishedUsesConditionsAndPhase(t *testing.T) {
	byCondition := &v1beta1.Job{Status: v1beta1.JobStatus{
		Conditions: []v1beta1.JobCondition{{Type: v1beta1.JobComplete, Status: "True"}},
	}}
	if !jobFinished(byCondition) {
		t.Fatal("Complete condition should mark a job finished")
	}

	byPhase := &v1beta1.Job{Status: v1beta1.JobStatus{Phase: v1beta1.JobPhaseFailed}}
	if !jobFinished(byPhase) {
		t.Fatal("Failed phase should mark a job finished")
	}

	running := &v1beta1.Job{Status: v1beta1.JobStatus{Phase: v1beta1.JobPhaseRunning}}
	if jobFinished(running) {
		t.Fatal("a running job is not finished")
	}
}

func TestDeterministicJobName(t *testing.T) {
	at := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)
	want := fmt.Sprintf("daily-%d", at.Unix()/60)
	if got := jobName("daily", at); got != want {
		t.Fatalf("jobName = %q, want %q", got, want)
	}
	if jobName("daily", at) != jobName("daily", at) {
		t.Fatal("jobName is not deterministic")
	}
}
