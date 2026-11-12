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

// Package job_test exercises the whole Schedule -> CronJob -> Job -> agentrun
// chain against controller-runtime's fake client. No API server or cluster is
// involved: the CronJob controller creates the child Job in the fake store, and
// the Job controller dispatches it through a fake agent run client.
package job_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cronjobcontroller "github.com/onexstack/onexai/internal/controller/job/cronjob"
	jobcontroller "github.com/onexstack/onexai/internal/controller/job/job"
	_ "github.com/onexstack/onexai/internal/controller/job/job/providers/all"
	"github.com/onexstack/onexai/internal/controller/job/job/providers/registry"
	batchv1beta1 "github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
)

type fakeAgentRun struct {
	created  []*registry.AgentRunRequest
	result   *registry.AgentRunResult
	status   *registry.AgentRunResult
	canceled []string
}

func (f *fakeAgentRun) CreateRun(_ context.Context, req *registry.AgentRunRequest) (*registry.AgentRunResult, error) {
	f.created = append(f.created, req)
	return f.result, nil
}

func (f *fakeAgentRun) GetRun(_ context.Context, _ string) (*registry.AgentRunResult, error) {
	return f.status, nil
}

func (f *fakeAgentRun) CancelRun(_ context.Context, id string) error {
	f.canceled = append(f.canceled, id)
	return nil
}

func TestScheduledRunCreatesCronJobWhichRunsAnAgentJob(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 9, 0, 30, 0, time.UTC)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	providerSpec, err := json.Marshal(batchv1beta1.AgentRunProviderSpec{
		Agent:            "ops-agent",
		Prompt:           "summarize yesterday",
		SubjectID:        "ou_1",
		Metering:         "none",
		ApprovalFallback: "deny",
		Source:           "scheduled-run",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The ScheduledRun row the REST facade creates. Its CronJob is created a
	// minute before now, so "* * * * *" is due at the current minute boundary.
	cronJob := &batchv1beta1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "daily",
			Namespace:         "default",
			UID:               types.UID("cronjob-uid"),
			CreationTimestamp: metav1.NewTime(now.Add(-90 * time.Second)),
		},
		Spec: batchv1beta1.CronJobSpec{
			Schedule:                "* * * * *",
			TimeZone:                ptr.To("UTC"),
			StartingDeadlineSeconds: ptr.To[int64](300),
			ConcurrencyPolicy:       batchv1beta1.ForbidConcurrent,
			Suspend:                 ptr.To(false),
			JobTemplate: batchv1beta1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"team": "sre"}},
				Spec: batchv1beta1.JobSpec{
					Type:         batchv1beta1.JobTypeAgentRun,
					BackoffLimit: ptr.To[int32](0),
					ProviderSpec: &runtime.RawExtension{Raw: providerSpec},
				},
			},
			SuccessfulJobsHistoryLimit: ptr.To[int32](10),
			FailedJobsHistoryLimit:     ptr.To[int32](5),
		},
	}

	fakeClient := newMemClient(scheme, cronJob)

	// 1) CronJob controller: the due occurrence becomes a deterministic child Job.
	cronJobReconciler := cronjobcontroller.NewReconciler(
		fakeClient, record.NewFakeRecorder(64), func() time.Time { return now },
	)
	if _, err := cronJobReconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "daily"},
	}); err != nil {
		t.Fatalf("cronjob reconcile: %v", err)
	}

	var jobs batchv1beta1.JobList
	if err := fakeClient.List(ctx, &jobs, client.InNamespace("default")); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected one child job, got %d", len(jobs.Items))
	}
	job := &jobs.Items[0]
	wantName := fmt.Sprintf("daily-%d", now.Truncate(time.Minute).Unix()/60)
	if job.Name != wantName {
		t.Fatalf("job name = %q, want %q", job.Name, wantName)
	}
	if job.Spec.Type != batchv1beta1.JobTypeAgentRun {
		t.Fatalf("job type = %q", job.Spec.Type)
	}
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].UID != cronJob.UID {
		t.Fatalf("job is not owned by the cronjob: %+v", job.OwnerReferences)
	}
	if string(job.Spec.ProviderSpec.Raw) != string(providerSpec) {
		t.Fatalf("providerSpec was not carried into the job: %s", job.Spec.ProviderSpec.Raw)
	}

	// 2) Job controller: first pass installs the finalizer, second dispatches.
	agentRun := &fakeAgentRun{
		result: &registry.AgentRunResult{ID: "run-1", SessionID: "session-1", State: "pending"},
	}
	jobReconciler := jobcontroller.NewReconciler(
		fakeClient, record.NewFakeRecorder(64), agentRun, time.Second, nil,
	)
	jobKey := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: job.Namespace, Name: job.Name}}
	if _, err := jobReconciler.Reconcile(ctx, jobKey); err != nil {
		t.Fatalf("job reconcile (finalizer): %v", err)
	}
	if _, err := jobReconciler.Reconcile(ctx, jobKey); err != nil {
		t.Fatalf("job reconcile (start): %v", err)
	}

	if len(agentRun.created) != 1 {
		t.Fatalf("expected one run submission, got %d", len(agentRun.created))
	}
	submitted := agentRun.created[0]
	if submitted.Agent != "ops-agent" || submitted.Prompt != "summarize yesterday" {
		t.Fatalf("wrong run payload: %+v", submitted)
	}
	if submitted.SubjectID != "ou_1" || submitted.Metering != "none" {
		t.Fatalf("identity/metering not preserved: %+v", submitted)
	}
	if submitted.Source != "scheduled-run" || submitted.DedupKey == "" {
		t.Fatalf("source/dedup not set: %+v", submitted)
	}

	var running batchv1beta1.Job
	if err := fakeClient.Get(ctx, jobKey.NamespacedName, &running); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if running.Status.Phase != batchv1beta1.JobPhaseRunning || running.Status.StartedAt == nil {
		t.Fatalf("job did not start: %+v", running.Status)
	}
	if running.Status.ProviderStatus == nil {
		t.Fatal("provider status was not persisted")
	}
	var runStatus batchv1beta1.AgentRunProviderStatus
	if err := json.Unmarshal(running.Status.ProviderStatus.Raw, &runStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus.RunID != "run-1" || runStatus.SessionID != "session-1" {
		t.Fatalf("run identity not recorded: %+v", runStatus)
	}

	// 3) The run finishes; the next poll settles the Job and writes the
	// standard Complete condition the CronJob controller uses for history.
	// "completed" is the durable run row's success spelling returned by
	// GET /v1/agent/runs/:id, which is what the provider actually polls.
	agentRun.status = &registry.AgentRunResult{ID: "run-1", State: "completed"}
	if _, err := jobReconciler.Reconcile(ctx, jobKey); err != nil {
		t.Fatalf("job reconcile (complete): %v", err)
	}

	var completed batchv1beta1.Job
	if err := fakeClient.Get(ctx, jobKey.NamespacedName, &completed); err != nil {
		t.Fatalf("get completed job: %v", err)
	}
	if completed.Status.Phase != batchv1beta1.JobPhaseSucceeded {
		t.Fatalf("phase = %q, want Succeeded", completed.Status.Phase)
	}
	if completed.Status.EndedAt == nil {
		t.Fatal("EndedAt was not set")
	}
	if !hasCondition(completed.Status.Conditions, batchv1beta1.JobComplete) {
		t.Fatalf("Complete condition missing: %+v", completed.Status.Conditions)
	}
}

func TestForbidPolicySkipsWhileAnOccurrenceIsRunning(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 9, 0, 30, 0, time.UTC)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	cronJob := &batchv1beta1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name: "daily", Namespace: "default", UID: types.UID("cronjob-uid"),
			CreationTimestamp: metav1.NewTime(now.Add(-90 * time.Second)),
		},
		Spec: batchv1beta1.CronJobSpec{
			Schedule:                "* * * * *",
			TimeZone:                ptr.To("UTC"),
			StartingDeadlineSeconds: ptr.To[int64](300),
			ConcurrencyPolicy:       batchv1beta1.ForbidConcurrent,
			Suspend:                 ptr.To(false),
			JobTemplate: batchv1beta1.JobTemplateSpec{
				Spec: batchv1beta1.JobSpec{Type: batchv1beta1.JobTypeAgentRun},
			},
		},
	}
	activeJob := &batchv1beta1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "daily-existing", Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: batchv1beta1.SchemeGroupVersion.String(),
				Kind:       "CronJob", Name: "daily", UID: types.UID("cronjob-uid"),
				Controller: ptr.To(true),
			}},
		},
		Status: batchv1beta1.JobStatus{Phase: batchv1beta1.JobPhaseRunning},
	}

	fakeClient := newMemClient(scheme, cronJob, activeJob)

	reconciler := cronjobcontroller.NewReconciler(
		fakeClient, record.NewFakeRecorder(64), func() time.Time { return now },
	)
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "daily"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var jobs batchv1beta1.JobList
	if err := fakeClient.List(ctx, &jobs, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("Forbid must not create an overlapping job, got %d jobs", len(jobs.Items))
	}
}

func hasCondition(conditions []batchv1beta1.JobCondition, typ batchv1beta1.JobConditionType) bool {
	for _, c := range conditions {
		if c.Type == typ && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
