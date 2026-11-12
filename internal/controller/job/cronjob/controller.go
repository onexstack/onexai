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

// Package cronjob reconciles batch.onex.io CronJobs. It computes the next
// occurrence, creates a deterministically named child Job, and enforces the
// concurrency policy, starting deadline and history limits.
package cronjob

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
)

const (
	// ControllerName is the name the controller registers under.
	ControllerName = "cronjob-controller"
	// DefaultStartingDeadlineSeconds is used when the spec leaves it unset.
	DefaultStartingDeadlineSeconds int64 = 300
)

var cronJobKind = v1beta1.SchemeGroupVersion.WithKind("CronJob")

// Reconciler reconciles a CronJob.
type Reconciler struct {
	client   client.Client
	recorder record.EventRecorder
	now      func() time.Time
}

// Add registers the controller with the manager.
func Add(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	return (&Reconciler{}).SetupWithManager(ctx, mgr, options)
}

// NewReconciler builds a reconciler with its dependencies already set. It is
// what the integration tests use: Reconcile can be called directly against a
// fake client, without a manager or a cluster.
func NewReconciler(c client.Client, recorder record.EventRecorder, now func() time.Time) *Reconciler {
	if now == nil {
		now = time.Now
	}
	return &Reconciler{client: c, recorder: recorder, now: now}
}

// SetupWithManager sets up the controller with the manager.
func (r *Reconciler) SetupWithManager(_ context.Context, mgr ctrl.Manager, options controller.Options) error {
	r.client = mgr.GetClient()
	r.recorder = mgr.GetEventRecorderFor(ControllerName)
	if r.now == nil {
		r.now = time.Now
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.CronJob{}).
		Owns(&v1beta1.Job{}).
		WithOptions(options).
		Named(ControllerName).
		Complete(r)
}

// Reconcile drives a CronJob towards its next occurrence.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cronJob := &v1beta1.CronJob{}
	if err := r.client.Get(ctx, req.NamespacedName, cronJob); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !cronJob.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	jobs, err := r.ownedJobs(ctx, cronJob)
	if err != nil {
		return ctrl.Result{}, err
	}

	changed := r.cleanupHistory(ctx, cronJob, jobs)
	jobs, err = r.ownedJobs(ctx, cronJob)
	if err != nil {
		return ctrl.Result{}, err
	}
	if activeChanged := r.syncActive(cronJob, jobs); activeChanged {
		changed = true
	}

	now := r.now()
	schedule, err := scheduleFor(cronJob)
	if err != nil {
		r.recorder.Eventf(cronJob, corev1.EventTypeWarning, "UnknownTimeZone", "invalid schedule or time zone: %v", err)
		if changed {
			return ctrl.Result{}, r.updateStatus(ctx, cronJob)
		}
		return ctrl.Result{}, nil
	}

	if cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend {
		if changed {
			return ctrl.Result{}, r.updateStatus(ctx, cronJob)
		}
		return ctrl.Result{}, nil
	}

	last := cronJob.CreationTimestamp.Time
	if cronJob.Status.LastScheduleTime != nil {
		last = cronJob.Status.LastScheduleTime.Time
	}
	next := schedule.Next(last)
	if next.IsZero() {
		if changed {
			return ctrl.Result{}, r.updateStatus(ctx, cronJob)
		}
		return ctrl.Result{}, nil
	}

	if now.Before(next) {
		if changed {
			if err := r.updateStatus(ctx, cronJob); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: next.Sub(now)}, nil
	}

	// The controller was down past the starting deadline. Advance the cursor
	// instead of replaying every missed occurrence.
	deadline := time.Duration(ptr.Deref(cronJob.Spec.StartingDeadlineSeconds, DefaultStartingDeadlineSeconds)) * time.Second
	if now.Sub(next) > deadline {
		cronJob.Status.LastScheduleTime = &metav1.Time{Time: now}
		r.recorder.Eventf(cronJob, corev1.EventTypeWarning, "MissedSchedule", "missed scheduled time %s", next.Format(time.RFC3339))
		if err := r.updateStatus(ctx, cronJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: schedule.Next(now).Sub(now)}, nil
	}

	if skipped, err := r.applyConcurrencyPolicy(ctx, cronJob, jobs); err != nil {
		return ctrl.Result{}, err
	} else if skipped {
		cronJob.Status.LastScheduleTime = &metav1.Time{Time: next}
		if err := r.updateStatus(ctx, cronJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: schedule.Next(next).Sub(now)}, nil
	}

	if err := r.createJob(ctx, cronJob, next); err != nil {
		return ctrl.Result{}, err
	}
	cronJob.Status.LastScheduleTime = &metav1.Time{Time: next}
	if err := r.updateStatus(ctx, cronJob); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: schedule.Next(next).Sub(now)}, nil
}

// scheduleFor parses the schedule with its IANA time zone. robfig's standard
// parser takes five fields; the time zone is applied with CRON_TZ.
func scheduleFor(cronJob *v1beta1.CronJob) (cron.Schedule, error) {
	spec := cronJob.Spec.Schedule
	if cronJob.Spec.TimeZone != nil && *cronJob.Spec.TimeZone != "" {
		if _, err := time.LoadLocation(*cronJob.Spec.TimeZone); err != nil {
			return nil, err
		}
		spec = "CRON_TZ=" + *cronJob.Spec.TimeZone + " " + spec
	}
	return cron.ParseStandard(spec)
}

// ownedJobs lists the Jobs controlled by the CronJob.
func (r *Reconciler) ownedJobs(ctx context.Context, cronJob *v1beta1.CronJob) ([]*v1beta1.Job, error) {
	var list v1beta1.JobList
	if err := r.client.List(ctx, &list, client.InNamespace(cronJob.Namespace)); err != nil {
		return nil, err
	}
	out := make([]*v1beta1.Job, 0, len(list.Items))
	for i := range list.Items {
		job := &list.Items[i]
		if metav1.IsControlledBy(job, cronJob) {
			out = append(out, job)
		}
	}
	return out, nil
}

// syncActive rebuilds Status.Active from the child Jobs that are still running.
// It reports whether the status changed.
func (r *Reconciler) syncActive(cronJob *v1beta1.CronJob, jobs []*v1beta1.Job) bool {
	active := make([]corev1.ObjectReference, 0, len(jobs))
	for _, job := range jobs {
		if jobFinished(job) {
			continue
		}
		active = append(active, corev1.ObjectReference{
			APIVersion:      v1beta1.SchemeGroupVersion.String(),
			Kind:            "Job",
			Namespace:       job.Namespace,
			Name:            job.Name,
			UID:             job.UID,
			ResourceVersion: job.ResourceVersion,
		})
	}
	if sameRefs(cronJob.Status.Active, active) {
		return false
	}
	cronJob.Status.Active = active
	return true
}

func sameRefs(a, b []corev1.ObjectReference) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].UID != b[i].UID || a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

// applyConcurrencyPolicy reports whether this occurrence must be skipped.
func (r *Reconciler) applyConcurrencyPolicy(
	ctx context.Context, cronJob *v1beta1.CronJob, jobs []*v1beta1.Job,
) (bool, error) {
	active := 0
	for _, job := range jobs {
		if !jobFinished(job) {
			active++
		}
	}
	if active == 0 {
		return false, nil
	}

	switch cronJob.Spec.ConcurrencyPolicy {
	case v1beta1.ForbidConcurrent, "":
		r.recorder.Event(cronJob, corev1.EventTypeWarning, "JobAlreadyActive", "previous job has not finished; skipping this occurrence")
		return true, nil
	case v1beta1.ReplaceConcurrent:
		for _, job := range jobs {
			if jobFinished(job) {
				continue
			}
			if err := r.client.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && client.IgnoreNotFound(err) != nil {
				return false, err
			}
		}
		r.recorder.Event(cronJob, corev1.EventTypeNormal, "Replaced", "replaced the active job")
		return false, nil
	default:
		return false, nil
	}
}

// createJob creates the occurrence. The name is deterministic, which makes a
// retried reconcile idempotent.
func (r *Reconciler) createJob(ctx context.Context, cronJob *v1beta1.CronJob, scheduledAt time.Time) error {
	job := &v1beta1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName(cronJob.Name, scheduledAt),
			Namespace: cronJob.Namespace,
			Labels:    copyMap(cronJob.Spec.JobTemplate.Labels),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(cronJob, cronJobKind),
			},
		},
		Spec: *cronJob.Spec.JobTemplate.Spec.DeepCopy(),
	}
	if len(cronJob.Spec.JobTemplate.Annotations) > 0 {
		job.Annotations = copyMap(cronJob.Spec.JobTemplate.Annotations)
	}
	// The timestamp is the planned firing time, not the creation time: the
	// controller may create the Job a little after the tick. The agentrun
	// provider forwards it so the async-task view can show the schedule time.
	if job.Annotations == nil {
		job.Annotations = map[string]string{}
	}
	job.Annotations[v1beta1.CronJobScheduledTimestampAnnotation] = scheduledAt.UTC().Format(time.RFC3339)

	if err := r.client.Create(ctx, job); err != nil {
		if client.IgnoreAlreadyExists(err) == nil {
			return nil
		}
		r.recorder.Eventf(cronJob, corev1.EventTypeWarning, "FailedCreate", "failed to create job: %v", err)
		return err
	}
	r.recorder.Eventf(cronJob, corev1.EventTypeNormal, "Scheduled", "created job %s", job.Name)
	return nil
}

// cleanupHistory deletes old finished Jobs according to the history limits.
func (r *Reconciler) cleanupHistory(ctx context.Context, cronJob *v1beta1.CronJob, jobs []*v1beta1.Job) bool {
	successLimit := int32(10)
	if cronJob.Spec.SuccessfulJobsHistoryLimit != nil {
		successLimit = *cronJob.Spec.SuccessfulJobsHistoryLimit
	}
	failedLimit := int32(5)
	if cronJob.Spec.FailedJobsHistoryLimit != nil {
		failedLimit = *cronJob.Spec.FailedJobsHistoryLimit
	}

	var succeeded, failed []*v1beta1.Job
	for _, job := range jobs {
		switch {
		case jobComplete(job):
			succeeded = append(succeeded, job)
		case jobFailed(job):
			failed = append(failed, job)
		}
	}

	changed := r.removeOldest(ctx, succeeded, int(successLimit))
	if r.removeOldest(ctx, failed, int(failedLimit)) {
		changed = true
	}
	return changed
}

func (r *Reconciler) removeOldest(ctx context.Context, jobs []*v1beta1.Job, keep int) bool {
	if len(jobs) <= keep {
		return false
	}
	sort.Slice(jobs, func(i, j int) bool {
		ti, tj := startedTime(jobs[i]), startedTime(jobs[j])
		if ti.Equal(tj) {
			return jobs[i].Name < jobs[j].Name
		}
		return ti.Before(tj)
	})
	changed := false
	for _, job := range jobs[:len(jobs)-keep] {
		if err := r.client.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && client.IgnoreNotFound(err) != nil {
			r.recorder.Eventf(job, corev1.EventTypeWarning, "CleanupFailed", "failed to delete job: %v", err)
			continue
		}
		changed = true
	}
	return changed
}

// jobName is deterministic for a scheduled minute; a retried reconcile that
// reaches the same occurrence cannot create a second Job.
func jobName(cronJob string, at time.Time) string {
	return fmt.Sprintf("%s-%d", cronJob, at.Unix()/60)
}

func startedTime(job *v1beta1.Job) time.Time {
	if job.Status.StartedAt != nil {
		return job.Status.StartedAt.Time
	}
	return job.CreationTimestamp.Time
}

func jobFinished(job *v1beta1.Job) bool {
	return jobComplete(job) || jobFailed(job)
}

func jobComplete(job *v1beta1.Job) bool {
	if job.Status.Phase == v1beta1.JobPhaseSucceeded {
		return true
	}
	return hasCondition(job, v1beta1.JobComplete)
}

func jobFailed(job *v1beta1.Job) bool {
	if job.Status.Phase == v1beta1.JobPhaseFailed {
		return true
	}
	return hasCondition(job, v1beta1.JobFailed)
}

func hasCondition(job *v1beta1.Job, typ v1beta1.JobConditionType) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == typ && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *Reconciler) updateStatus(ctx context.Context, cronJob *v1beta1.CronJob) error {
	return r.client.Status().Update(ctx, cronJob)
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

var _ runtime.Object = &v1beta1.CronJob{}
