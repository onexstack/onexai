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
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	batchv1beta1 "github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// Labels and annotations the facade writes on an occurrence.
const (
	TriggerAnnotation = "onexai.io/trigger"
	ScheduleLabel     = "onexai.io/schedule"
)

// ExecutionStore lists and creates the Jobs a CronJob owns. It is the
// occurrence history behind the ScheduledRun REST facade.
type ExecutionStore struct {
	client    *declclient.Client
	namespace string
}

var _ store.ScheduledExecutionStore = (*ExecutionStore)(nil)

// NewExecutionStore builds the batch-backed occurrence store.
func NewExecutionStore(client *declclient.Client, namespace string) *ExecutionStore {
	return &ExecutionStore{client: client, namespace: namespace}
}

// ListExecution returns the Job rows owned by schedule, newest first.
func (s *ExecutionStore) ListExecution(
	ctx context.Context, namespace, schedule string, limit int,
) ([]*model.ScheduledExecutionM, error) {
	ns := rowNamespace(namespace, s.namespace)
	list, err := s.client.Clientset.BatchV1beta1().Jobs(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]*model.ScheduledExecutionM, 0, len(list.Items))
	for i := range list.Items {
		job := &list.Items[i]
		if scheduleOf(job) != schedule {
			continue
		}
		out = append(out, executionFromJob(job))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetExecution returns one occurrence by Job name.
func (s *ExecutionStore) GetExecution(ctx context.Context, id string) (*model.ScheduledExecutionM, error) {
	job, err := s.getJob(ctx, id)
	if err != nil {
		return nil, err
	}
	return executionFromJob(job), nil
}

// CreateManualExecution clones the CronJob's JobTemplate into a one-off Job.
// The deterministic-looking prefix plus a millisecond suffix keeps the name
// within the DNS label limit while allowing back-to-back triggers.
func (s *ExecutionStore) CreateManualExecution(
	ctx context.Context, namespace, schedule string,
) (*model.ScheduledExecutionM, error) {
	ns := rowNamespace(namespace, s.namespace)
	cronJob, err := s.findCronJob(ctx, ns, schedule)
	if err != nil {
		return nil, err
	}

	labels := copyStringMap(cronJob.Spec.JobTemplate.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ScheduleLabel] = cronJob.Name
	annotations := copyStringMap(cronJob.Spec.JobTemplate.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[TriggerAnnotation] = "manual"
	annotations[ScheduleLabel] = cronJob.Name

	job := &batchv1beta1.Job{
		ObjectMeta: metav1.ObjectMeta{
			// CronJob names allow 52 characters; "%.45s-m-<13 digits>" stays
			// below the 63-character Job name limit.
			Name:            fmt.Sprintf("%.45s-m-%d", cronJob.Name, time.Now().UnixMilli()),
			Namespace:       cronJob.Namespace,
			Labels:          labels,
			Annotations:     annotations,
			OwnerReferences: controllerRef(cronJob),
		},
		Spec: *cronJob.Spec.JobTemplate.Spec.DeepCopy(),
	}
	if job.Spec.Type == "" {
		job.Spec.Type = batchv1beta1.JobTypeAgentRun
	}

	created, err := s.client.Clientset.BatchV1beta1().Jobs(job.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return executionFromJob(created), nil
}

// CancelExecution deletes the Job. The Job controller's finalizer stops the
// agent run before the object goes away.
func (s *ExecutionStore) CancelExecution(ctx context.Context, id string) error {
	job, err := s.getJob(ctx, id)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	policy := metav1.DeletePropagationBackground
	return s.client.Clientset.BatchV1beta1().Jobs(job.Namespace).Delete(ctx, job.Name, metav1.DeleteOptions{
		PropagationPolicy: &policy,
	})
}

func (s *ExecutionStore) findCronJob(
	ctx context.Context, namespace, name string,
) (*batchv1beta1.CronJob, error) {
	if namespace != "" {
		return s.client.Clientset.BatchV1beta1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	list, err := s.client.Clientset.BatchV1beta1().CronJobs(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Name == name {
			return &list.Items[i], nil
		}
	}
	return nil, apierrors.NewNotFound(batchv1beta1.Resource("cronjobs"), name)
}

func (s *ExecutionStore) getJob(ctx context.Context, id string) (*batchv1beta1.Job, error) {
	if s.namespace != "" {
		return s.client.Clientset.BatchV1beta1().Jobs(s.namespace).Get(ctx, id, metav1.GetOptions{})
	}
	// No namespace configured: the REST request carries only the execution ID,
	// so find the Job across namespaces.
	list, err := s.client.Clientset.BatchV1beta1().Jobs(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + id,
	})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, apierrors.NewNotFound(batchv1beta1.Resource("jobs"), id)
	}
	return &list.Items[0], nil
}

func executionFromJob(job *batchv1beta1.Job) *model.ScheduledExecutionM {
	execution := &model.ScheduledExecutionM{
		ID:        job.Name,
		Namespace: job.Namespace,
		Schedule:  scheduleOf(job),
		Trigger:   job.Annotations[TriggerAnnotation],
		State:     stateOf(job),
		CreatedAt: job.CreationTimestamp.UnixMilli(),
	}
	if execution.Trigger == "" {
		execution.Trigger = "scheduled"
	}
	if job.Status.StartedAt != nil {
		execution.StartedAt = job.Status.StartedAt.UnixMilli()
	}
	if job.Status.EndedAt != nil {
		execution.FinishedAt = job.Status.EndedAt.UnixMilli()
	}
	if job.Status.ErrorMessage != nil {
		execution.FailureReason = *job.Status.ErrorMessage
	}

	var spec batchv1beta1.AgentRunProviderSpec
	if job.Spec.ProviderSpec != nil && len(job.Spec.ProviderSpec.Raw) > 0 {
		if err := json.Unmarshal(job.Spec.ProviderSpec.Raw, &spec); err == nil {
			execution.Prompt = spec.Prompt
		}
	}
	var status batchv1beta1.AgentRunProviderStatus
	if job.Status.ProviderStatus != nil && len(job.Status.ProviderStatus.Raw) > 0 {
		if err := json.Unmarshal(job.Status.ProviderStatus.Raw, &status); err == nil {
			execution.RunID = status.RunID
			execution.SessionID = status.SessionID
			if execution.FailureReason == "" {
				execution.FailureReason = status.Message
			}
		}
	}
	return execution
}

func scheduleOf(job *batchv1beta1.Job) string {
	if v := job.Annotations[ScheduleLabel]; v != "" {
		return v
	}
	if v := job.Labels[ScheduleLabel]; v != "" {
		return v
	}
	for _, ref := range job.OwnerReferences {
		if ref.Kind == "CronJob" {
			return ref.Name
		}
	}
	return ""
}

func stateOf(job *batchv1beta1.Job) string {
	if !job.DeletionTimestamp.IsZero() {
		return model.ExecutionStateCanceled
	}
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1beta1.JobComplete:
			return model.ExecutionStateCompleted
		case batchv1beta1.JobFailed:
			return model.ExecutionStateFailed
		}
	}
	switch job.Status.Phase {
	case batchv1beta1.JobPhaseSucceeded:
		return model.ExecutionStateCompleted
	case batchv1beta1.JobPhaseFailed:
		return model.ExecutionStateFailed
	case batchv1beta1.JobPhaseRunning:
		return model.ExecutionStateRunning
	case batchv1beta1.JobPhasePending, "":
		// A Job the controller has not started yet carries no phase. Reporting
		// that as running is what makes a controller that is down (or failing
		// to dispatch) indistinguishable from a run in progress.
		return model.ExecutionStatePending
	default:
		return model.ExecutionStateRunning
	}
}

func controllerRef(cronJob *batchv1beta1.CronJob) []metav1.OwnerReference {
	return []metav1.OwnerReference{{
		APIVersion: batchv1beta1.SchemeGroupVersion.String(),
		Kind:       "CronJob",
		Name:       cronJob.Name,
		UID:        cronJob.UID,
		Controller: ptrBool(true),
	}}
}

func ptrBool(v bool) *bool { return &v }
