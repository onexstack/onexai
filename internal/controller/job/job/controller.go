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

// Package job reconciles batch.onex.io Jobs. It owns the Job lifecycle
// (Pending -> Running -> Succeeded/Failed, with retries) and delegates the
// actual workload to the provider registered for Job.Spec.Type.
package job

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/onexstack/onexai/internal/controller/job/job/providers/registry"
	"github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
)

const (
	// ControllerName is the name the controller registers under.
	ControllerName = "job-controller"
	// Finalizer is present while the controller may still need to stop a
	// running provider workload.
	Finalizer = "batch.onex.io/job-finalizer"
	// DefaultPollInterval is how often a running job re-checks its provider.
	DefaultPollInterval = 2 * time.Second
)

// Reconciler reconciles a Job.
type Reconciler struct {
	client       client.Client
	recorder     record.EventRecorder
	providers    map[v1beta1.JobType]registry.Provider
	agentRun     registry.AgentRunClient
	pollInterval time.Duration
	jobTypes     map[v1beta1.JobType]bool
	now          func() time.Time
}

// Add registers the controller with the manager.
func Add(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	return (&Reconciler{}).SetupWithManager(ctx, mgr, options)
}

// NewReconciler builds a reconciler with its dependencies already set. It is
// what the integration tests use: Reconcile can be called directly against a
// fake client, without a manager or a cluster.
func NewReconciler(
	c client.Client, recorder record.EventRecorder, agentRun registry.AgentRunClient,
	poll time.Duration, jobTypes []v1beta1.JobType,
) *Reconciler {
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	r := &Reconciler{
		client:       c,
		recorder:     recorder,
		agentRun:     agentRun,
		pollInterval: poll,
		now:          time.Now,
	}
	r.providers = registry.Build(registry.Deps{Client: c, AgentRun: agentRun, PollInterval: poll})
	return r.WithJobTypes(jobTypes)
}

// SetupWithManager sets up the controller with the manager.
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	r.client = mgr.GetClient()
	r.recorder = mgr.GetEventRecorderFor(ControllerName)
	if r.pollInterval <= 0 {
		r.pollInterval = DefaultPollInterval
	}
	if r.now == nil {
		r.now = time.Now
	}
	r.providers = registry.Build(registry.Deps{
		Client:       r.client,
		AgentRun:     r.agentRun,
		PollInterval: r.pollInterval,
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Job{}).
		WithOptions(options).
		Named(ControllerName).
		Complete(r)
}

// WithAgentRunClient injects the asynchronous agent run client.
func (r *Reconciler) WithAgentRunClient(c registry.AgentRunClient) *Reconciler {
	r.agentRun = c
	return r
}

// WithPollInterval overrides the provider status poll interval.
func (r *Reconciler) WithPollInterval(d time.Duration) *Reconciler {
	r.pollInterval = d
	return r
}

// WithJobTypes restricts reconciliation to the given types. Empty means all.
func (r *Reconciler) WithJobTypes(types []v1beta1.JobType) *Reconciler {
	if len(types) == 0 {
		return r
	}
	r.jobTypes = make(map[v1beta1.JobType]bool, len(types))
	for _, t := range types {
		r.jobTypes[t] = true
	}
	return r
}

// Reconcile drives a Job towards its terminal state.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	job := &v1beta1.Job{}
	if err := r.client.Get(ctx, req.NamespacedName, job); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !r.handles(job) {
		return ctrl.Result{}, nil
	}

	if !job.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, job)
	}
	if !controllerutil.ContainsFinalizer(job, Finalizer) {
		controllerutil.AddFinalizer(job, Finalizer)
		if err := r.client.Update(ctx, job); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	return r.sync(ctx, job)
}

// handles reports whether this controller instance owns the Job's type.
func (r *Reconciler) handles(job *v1beta1.Job) bool {
	if len(r.jobTypes) == 0 {
		return true
	}
	return r.jobTypes[job.Spec.Type]
}

func (r *Reconciler) finalize(ctx context.Context, job *v1beta1.Job) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(job, Finalizer) {
		return ctrl.Result{}, nil
	}
	if p := r.providerFor(job); p != nil {
		if err := p.Stop(ctx, job); err != nil {
			r.recorder.Eventf(job, corev1.EventTypeWarning, "StopFailed", "failed to stop job: %v", err)
			return ctrl.Result{}, err
		}
	}
	controllerutil.RemoveFinalizer(job, Finalizer)
	if err := r.client.Update(ctx, job); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) sync(ctx context.Context, job *v1beta1.Job) (ctrl.Result, error) {
	provider := r.providerFor(job)
	if provider == nil {
		message := fmt.Sprintf("no provider registered for job type %q", job.Spec.Type)
		job.Status.ObservedGeneration = job.Generation
		job.Status.Phase = v1beta1.JobPhaseFailed
		job.Status.ErrorMessage = ptr.To(message)
		setCondition(job, v1beta1.JobFailed, corev1.ConditionTrue, "UnsupportedJobType", message)
		r.recorder.Event(job, corev1.EventTypeWarning, "UnsupportedJobType", message)
		return ctrl.Result{}, r.updateStatus(ctx, job)
	}

	job.Status.ObservedGeneration = job.Generation

	if job.Spec.Suspend {
		if err := provider.Stop(ctx, job); err != nil {
			r.recorder.Eventf(job, corev1.EventTypeWarning, "StopFailed", "failed to stop suspended job: %v", err)
			return ctrl.Result{}, err
		}
		job.Status.Phase = v1beta1.JobPhasePending
		job.Status.StartedAt = nil
		setCondition(job, v1beta1.JobSuspended, corev1.ConditionTrue, "JobSuspended", "Job is suspended")
		return ctrl.Result{}, r.updateStatus(ctx, job)
	}
	removeCondition(job, v1beta1.JobSuspended)

	if isFinished(job) {
		return ctrl.Result{}, nil
	}

	if job.Spec.ActiveDeadlineSeconds != nil && job.Status.StartedAt != nil {
		deadline := job.Status.StartedAt.Add(time.Duration(*job.Spec.ActiveDeadlineSeconds) * time.Second)
		if r.now().After(deadline) {
			message := fmt.Sprintf("job exceeded its activeDeadlineSeconds of %d", *job.Spec.ActiveDeadlineSeconds)
			_ = provider.Stop(ctx, job)
			r.fail(ctx, job, "DeadlineExceeded", message)
			return ctrl.Result{}, r.updateStatus(ctx, job)
		}
	}

	if job.Status.StartedAt == nil {
		if err := provider.Start(ctx, job); err != nil {
			r.recorder.Eventf(job, corev1.EventTypeWarning, "StartFailed", "failed to start job: %v", err)
			return ctrl.Result{}, err
		}
		now := metav1.Now()
		job.Status.StartedAt = &now
		job.Status.Phase = v1beta1.JobPhaseRunning
		r.recorder.Event(job, corev1.EventTypeNormal, "Started", "Started job")
		return ctrl.Result{RequeueAfter: r.pollInterval}, r.updateStatus(ctx, job)
	}

	status, err := provider.Status(ctx, job)
	if err != nil {
		return ctrl.Result{}, err
	}

	switch {
	case status.Succeeded:
		now := metav1.Now()
		job.Status.Phase = v1beta1.JobPhaseSucceeded
		job.Status.EndedAt = &now
		job.Status.ErrorMessage = nil
		setCondition(job, v1beta1.JobComplete, corev1.ConditionTrue, "JobComplete", status.Message)
		removeCondition(job, v1beta1.JobFailed)
		r.recorder.Event(job, corev1.EventTypeNormal, "Completed", "Job completed")
		return ctrl.Result{}, r.updateStatus(ctx, job)
	case status.Finished:
		if retried, err := r.retryOrFail(ctx, job, status.Message); err != nil || retried {
			return ctrl.Result{RequeueAfter: r.pollInterval}, err
		}
		return ctrl.Result{}, r.updateStatus(ctx, job)
	default:
		return ctrl.Result{RequeueAfter: r.pollInterval}, nil
	}
}

// retryOrFail increments the attempt and resets the run state while retries
// remain; otherwise it records a terminal failure.
func (r *Reconciler) retryOrFail(ctx context.Context, job *v1beta1.Job, message string) (bool, error) {
	limit := int32(0)
	if job.Spec.BackoffLimit != nil {
		limit = *job.Spec.BackoffLimit
	}
	if job.Status.Attempt < limit {
		job.Status.Attempt++
		job.Status.StartedAt = nil
		job.Status.ProviderStatus = nil
		job.Status.Phase = v1beta1.JobPhasePending
		removeCondition(job, v1beta1.JobComplete)
		removeCondition(job, v1beta1.JobFailed)
		r.recorder.Eventf(job, corev1.EventTypeWarning, "Retrying", "attempt %d failed: %s", job.Status.Attempt, message)
		if err := r.updateStatus(ctx, job); err != nil {
			return true, err
		}
		return true, nil
	}

	r.fail(ctx, job, "JobFailed", message)
	return false, nil
}

func (r *Reconciler) fail(_ context.Context, job *v1beta1.Job, reason, message string) {
	now := metav1.Now()
	job.Status.Phase = v1beta1.JobPhaseFailed
	job.Status.EndedAt = &now
	job.Status.ErrorMessage = ptr.To(message)
	setCondition(job, v1beta1.JobFailed, corev1.ConditionTrue, reason, message)
	removeCondition(job, v1beta1.JobComplete)
	r.recorder.Event(job, corev1.EventTypeWarning, reason, message)
}

func (r *Reconciler) updateStatus(ctx context.Context, job *v1beta1.Job) error {
	return r.client.Status().Update(ctx, job)
}

func (r *Reconciler) providerFor(job *v1beta1.Job) registry.Provider {
	if p, ok := r.providers[job.Spec.Type]; ok {
		return p
	}
	return nil
}

func isFinished(job *v1beta1.Job) bool {
	for _, c := range job.Status.Conditions {
		if (c.Type == v1beta1.JobComplete || c.Type == v1beta1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func setCondition(job *v1beta1.Job, typ v1beta1.JobConditionType, status corev1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type != typ {
			continue
		}
		if job.Status.Conditions[i].Status != status {
			job.Status.Conditions[i].LastTransitionTime = now
		}
		job.Status.Conditions[i].Status = status
		job.Status.Conditions[i].Reason = reason
		job.Status.Conditions[i].Message = message
		job.Status.Conditions[i].LastProbeTime = now
		return
	}
	job.Status.Conditions = append(job.Status.Conditions, v1beta1.JobCondition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastProbeTime:      now,
		LastTransitionTime: now,
	})
}

func removeCondition(job *v1beta1.Job, typ v1beta1.JobConditionType) {
	out := job.Status.Conditions[:0]
	for _, c := range job.Status.Conditions {
		if c.Type != typ {
			out = append(out, c)
		}
	}
	job.Status.Conditions = out
}
