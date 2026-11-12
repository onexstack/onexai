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

package agentrun

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/onexstack/onexai/internal/controller/job/job/providers/registry"
	"github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
)

type fakeClient struct {
	created  []*registry.AgentRunRequest
	result   *registry.AgentRunResult
	status   *registry.AgentRunResult
	err      error
	canceled []string
}

func (f *fakeClient) CreateRun(_ context.Context, req *registry.AgentRunRequest) (*registry.AgentRunResult, error) {
	f.created = append(f.created, req)
	return f.result, f.err
}

func (f *fakeClient) GetRun(_ context.Context, _ string) (*registry.AgentRunResult, error) {
	return f.status, f.err
}

func (f *fakeClient) CancelRun(_ context.Context, id string) error {
	f.canceled = append(f.canceled, id)
	return f.err
}

func testJob(t *testing.T) *v1beta1.Job {
	t.Helper()
	spec := v1beta1.AgentRunProviderSpec{
		Agent:     "ops-agent",
		Prompt:    "summarize yesterday",
		SubjectID: "ou_1",
		Metering:  "none",
		Source:    "scheduled-run",
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	return &v1beta1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "daily",
			UID:       types.UID("uid-1"),
		},
		Spec: v1beta1.JobSpec{
			Type:         v1beta1.JobTypeAgentRun,
			ProviderSpec: &runtime.RawExtension{Raw: raw},
		},
	}
}

func TestStartSubmitsWithOwnerAndMetering(t *testing.T) {
	fake := &fakeClient{result: &registry.AgentRunResult{ID: "run-1", SessionID: "sess-1", State: "pending"}}
	p := New(fake)
	job := testJob(t)

	if err := p.Start(context.Background(), job); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fake.created) != 1 {
		t.Fatalf("expected one submission, got %d", len(fake.created))
	}
	req := fake.created[0]
	if req.Agent != "ops-agent" || req.Prompt != "summarize yesterday" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.SubjectID != "ou_1" {
		t.Fatalf("subject not preserved: %+v", req)
	}
	if req.Metering != "none" {
		t.Fatalf("metering not preserved: %q", req.Metering)
	}
	if req.DedupKey == "" {
		t.Fatal("dedup key is empty")
	}

	var st v1beta1.AgentRunProviderStatus
	if err := registry.DecodeStatus(job, &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st.RunID != "run-1" || st.SessionID != "sess-1" {
		t.Fatalf("status not persisted: %+v", st)
	}
}

func TestStartForwardsSchedulerContext(t *testing.T) {
	fake := &fakeClient{result: &registry.AgentRunResult{ID: "run-1"}}
	p := New(fake)

	job := testJob(t)
	job.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "batch.onex.ai/v1beta1",
		Kind:       "CronJob",
		Name:       "daily-report",
		UID:        types.UID("cronjob-1"),
		Controller: ptr.To(true),
	}}
	job.Annotations = map[string]string{
		v1beta1.CronJobScheduledTimestampAnnotation: "2026-11-07T01:00:00Z",
	}
	job.Status.Attempt = 2

	if err := p.Start(context.Background(), job); err != nil {
		t.Fatalf("Start: %v", err)
	}
	req := fake.created[0]
	if req.ScheduleRef != "daily-report" {
		t.Fatalf("schedule ref = %q, want daily-report", req.ScheduleRef)
	}
	if req.Trigger != "scheduled" {
		t.Fatalf("trigger = %q, want scheduled", req.Trigger)
	}
	if req.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", req.Attempt)
	}
	want := time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC).UnixMilli()
	if req.ScheduledTime != want {
		t.Fatalf("scheduled time = %d, want %d", req.ScheduledTime, want)
	}
}

func TestStartMarksManualTrigger(t *testing.T) {
	fake := &fakeClient{result: &registry.AgentRunResult{ID: "run-1"}}
	p := New(fake)

	job := testJob(t)
	job.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "batch.onex.ai/v1beta1",
		Kind:       "CronJob",
		Name:       "daily-report",
		UID:        types.UID("cronjob-1"),
		Controller: ptr.To(true),
	}}
	job.Annotations = map[string]string{"onexai.io/trigger": "manual"}

	if err := p.Start(context.Background(), job); err != nil {
		t.Fatalf("Start: %v", err)
	}
	req := fake.created[0]
	if req.Trigger != "manual" {
		t.Fatalf("trigger = %q, want manual", req.Trigger)
	}
	if req.ScheduleRef != "daily-report" {
		t.Fatalf("schedule ref = %q, want daily-report", req.ScheduleRef)
	}
}

func TestStartDedupKeyChangesWithAttempt(t *testing.T) {
	fake := &fakeClient{result: &registry.AgentRunResult{ID: "run-1"}}
	p := New(fake)

	first := testJob(t)
	if err := p.Start(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := testJob(t)
	second.Status.Attempt = 1
	if err := p.Start(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if fake.created[0].DedupKey == fake.created[1].DedupKey {
		t.Fatal("a retry attempt must use a different dedup key")
	}
}

func TestStartRequiresSubject(t *testing.T) {
	fake := &fakeClient{result: &registry.AgentRunResult{ID: "run-1"}}
	p := New(fake)
	job := testJob(t)
	spec := v1beta1.AgentRunProviderSpec{Agent: "a", Prompt: "p"}
	raw, _ := json.Marshal(spec)
	job.Spec.ProviderSpec = &runtime.RawExtension{Raw: raw}

	if err := p.Start(context.Background(), job); err == nil {
		t.Fatal("expected an error for a missing subject")
	}
	if len(fake.created) != 0 {
		t.Fatal("nothing should be submitted without a subject")
	}
}

func TestStatusMapsTerminalStates(t *testing.T) {
	tests := []struct {
		state string
		want  registry.Status
	}{
		{"pending", registry.Status{}},
		{"running", registry.Status{}},
		// The durable run row spells success "completed"; the admission
		// vocabulary spells it "succeeded". Both must settle the Job.
		{"completed", registry.Status{Finished: true, Succeeded: true}},
		{"succeeded", registry.Status{Finished: true, Succeeded: true}},
		// A scheduled occurrence cannot answer an interactive pause, so the
		// provider stops the run and settles the Job instead of holding the
		// schedule's Forbid slot for a day.
		{"interrupted", registry.Status{Finished: true}},
		{"failed", registry.Status{Finished: true}},
		{"canceled", registry.Status{Finished: true}},
	}
	for _, tc := range tests {
		t.Run(tc.state, func(t *testing.T) {
			fake := &fakeClient{
				result: &registry.AgentRunResult{ID: "run-1"},
				status: &registry.AgentRunResult{ID: "run-1", State: tc.state},
			}
			p := New(fake)
			job := testJob(t)
			if err := p.Start(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			got, err := p.Status(context.Background(), job)
			if err != nil {
				t.Fatal(err)
			}
			if got.Finished != tc.want.Finished || got.Succeeded != tc.want.Succeeded {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if tc.state == "interrupted" && len(fake.canceled) != 1 {
				t.Fatalf("interrupted run was not stopped: canceled=%v", fake.canceled)
			}
		})
	}
}

// jobWithRunID builds a started Job whose provider status already names a run,
// which is the state Status polls after a successful Start.
func jobWithRunID(t *testing.T, runID string, started *metav1.Time) *v1beta1.Job {
	t.Helper()
	job := testJob(t)
	raw, err := json.Marshal(v1beta1.AgentRunProviderStatus{RunID: runID})
	if err != nil {
		t.Fatalf("marshal provider status: %v", err)
	}
	job.Status.ProviderStatus = &runtime.RawExtension{Raw: raw}
	job.Status.StartedAt = started
	return job
}

func TestStatusSettlesADisappearedRun(t *testing.T) {
	started := metav1.NewTime(time.Now().Add(-time.Minute))
	job := jobWithRunID(t, "run-1", &started)
	p := New(&fakeClient{err: registry.ErrRunNotFound})

	got, err := p.Status(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Finished || got.Succeeded {
		t.Fatalf("status = %+v, want a terminal failure", got)
	}
	if got.Message == "" {
		t.Fatal("a settled job must say why")
	}
}

func TestStatusKeepsPollingInsideTheNotFoundGrace(t *testing.T) {
	started := metav1.NewTime(time.Now())
	job := jobWithRunID(t, "run-1", &started)
	p := New(&fakeClient{err: registry.ErrRunNotFound})

	got, err := p.Status(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if got.Finished {
		t.Fatalf("status = %+v, want the job to keep polling during the grace window", got)
	}
}

func TestStopIgnoresADisappearedRun(t *testing.T) {
	job := jobWithRunID(t, "run-1", nil)
	p := New(&fakeClient{err: registry.ErrRunNotFound})

	if err := p.Stop(context.Background(), job); err != nil {
		t.Fatalf("a run that is already gone must not block the finalizer: %v", err)
	}
}

func TestStopCancelsStoredRun(t *testing.T) {
	fake := &fakeClient{result: &registry.AgentRunResult{ID: "run-1"}}
	p := New(fake)
	job := testJob(t)
	if err := p.Start(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(fake.canceled) != 1 || fake.canceled[0] != "run-1" {
		t.Fatalf("unexpected cancels: %+v", fake.canceled)
	}
}
