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

	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	batchv1beta1 "github.com/onexstack/onexai/pkg/apis/batch/v1beta1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// The two annotations the scheduled-run facade uses to recognize its own
// CronJobs. A batch CronJob without ManagedBy is left alone: it belongs to
// another caller, not to a migrated ScheduledRun.
const (
	ManagedByAnnotation    = "onexai.io/managed-by"
	ManagedByScheduled     = "scheduledrun"
	MigratedFromAnnotation = "onexai.io/migrated-from"
)

// BatchScheduledRunStore serves the ScheduledRun REST facade from
// batch.onex.io CronJobs. It is the replacement for the CRD-backed store: the
// public API is unchanged, while scheduling and history are owned by the batch
// controllers.
type BatchScheduledRunStore struct {
	client    *declclient.Client
	namespace string
}

var _ store.ScheduledRunStore = (*BatchScheduledRunStore)(nil)

// NewScheduledRunStore builds the batch-backed schedule accessor.
func NewScheduledRunStore(client *declclient.Client, namespace string) *BatchScheduledRunStore {
	return &BatchScheduledRunStore{client: client, namespace: namespace}
}

// Create implements ScheduledRunStore.
func (s *BatchScheduledRunStore) Create(ctx context.Context, row *model.ScheduledRunM) error {
	cronJob, err := cronJobFromRow(row, s.namespace)
	if err != nil {
		return err
	}
	if _, err := s.client.Clientset.BatchV1beta1().CronJobs(cronJob.Namespace).Create(ctx, cronJob, metav1.CreateOptions{}); err != nil {
		return err
	}
	// Report the namespace actually used (the default when the caller sent
	// none) instead of echoing the empty input back.
	row.Namespace = cronJob.Namespace
	return nil
}

// Update implements ScheduledRunStore. It patches the spec in place and leaves
// the controller-owned status untouched.
func (s *BatchScheduledRunStore) Update(ctx context.Context, row *model.ScheduledRunM) error {
	cronJob, err := s.findCronJob(ctx, rowNamespace(row.Namespace, s.namespace), row.Name)
	if err != nil {
		return err
	}
	ns := cronJob.Namespace
	updated, err := applyRowToCronJob(row, cronJob)
	if err != nil {
		return err
	}
	if _, err = s.client.Clientset.BatchV1beta1().CronJobs(ns).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return err
	}
	row.Namespace = ns
	return nil
}

// Delete implements ScheduledRunStore.
func (s *BatchScheduledRunStore) Delete(ctx context.Context, opts *where.Options) error {
	name := filterString(opts, "name")
	ns := filterNamespace(opts, s.namespace)
	if name == "" {
		return fmt.Errorf("scheduled run delete: name is required")
	}
	cronJob, err := s.findCronJob(ctx, ns, name)
	if err != nil {
		return err
	}
	return s.client.Clientset.BatchV1beta1().CronJobs(cronJob.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// Get implements ScheduledRunStore. An absent object is (nil, nil), which is
// what the REST layer turns into a 404.
func (s *BatchScheduledRunStore) Get(ctx context.Context, opts *where.Options) (*model.ScheduledRunM, error) {
	name := filterString(opts, "name")
	ns := filterNamespace(opts, s.namespace)
	if name == "" {
		return nil, nil
	}
	cronJob, err := s.findCronJob(ctx, ns, name)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !isScheduledRun(cronJob) {
		return nil, nil
	}
	return rowFromCronJob(cronJob), nil
}

// List implements ScheduledRunStore. It returns only CronJobs this facade owns.
func (s *BatchScheduledRunStore) List(ctx context.Context, opts *where.Options) (int64, []*model.ScheduledRunM, error) {
	ns := filterNamespace(opts, s.namespace)
	list, err := s.client.Clientset.BatchV1beta1().CronJobs(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, nil, err
	}
	rows := make([]*model.ScheduledRunM, 0, len(list.Items))
	for i := range list.Items {
		if !isScheduledRun(&list.Items[i]) {
			continue
		}
		rows = append(rows, rowFromCronJob(&list.Items[i]))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	total := int64(len(rows))
	if opts != nil {
		start := opts.Offset
		if start > len(rows) {
			start = len(rows)
		}
		end := start + opts.Limit
		if opts.Limit <= 0 || end > len(rows) {
			end = len(rows)
		}
		rows = rows[start:end]
	}
	return total, rows, nil
}

// UpdateStatus is a no-op: the batch CronJob controller owns status. The
// facade reads LastScheduleTime/Active from the CronJob instead.
func (s *BatchScheduledRunStore) UpdateStatus(context.Context, string, string, int64) error {
	return nil
}

// findCronJob resolves a name in a namespace, or across every namespace when
// the store is configured cluster-wide. A namespaced Get with an empty
// namespace is not a valid URL, which is why the fallback exists.
func (s *BatchScheduledRunStore) findCronJob(
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

func isScheduledRun(cronJob *batchv1beta1.CronJob) bool {
	return cronJob.Annotations[ManagedByAnnotation] == ManagedByScheduled ||
		cronJob.Annotations[MigratedFromAnnotation] != ""
}

func cronJobFromRow(row *model.ScheduledRunM, defaultNamespace string) (*batchv1beta1.CronJob, error) {
	cronJob := &batchv1beta1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        row.Name,
			Namespace:   rowNamespace(row.Namespace, defaultNamespace),
			Labels:      copyStringMap(row.Labels),
			Annotations: map[string]string{ManagedByAnnotation: ManagedByScheduled},
		},
		Spec: batchv1beta1.CronJobSpec{
			Schedule:                   row.Schedule,
			TimeZone:                   ptr.To(defaultTimeZone(row.TimeZone)),
			StartingDeadlineSeconds:    ptr.To[int64](300),
			ConcurrencyPolicy:          batchv1beta1.ForbidConcurrent,
			Suspend:                    ptr.To(suspended(row)),
			SuccessfulJobsHistoryLimit: ptr.To[int32](10),
			FailedJobsHistoryLimit:     ptr.To[int32](5),
		},
	}
	if row.DisplayName != "" {
		cronJob.Annotations["onexai.io/display-name"] = row.DisplayName
	}
	if row.Description != "" {
		cronJob.Annotations["onexai.io/description"] = row.Description
	}
	if err := setJobTemplate(cronJob, row); err != nil {
		return nil, err
	}
	return cronJob, nil
}

// applyRowToCronJob mutates an existing CronJob's spec from the REST row.
func applyRowToCronJob(row *model.ScheduledRunM, cronJob *batchv1beta1.CronJob) (*batchv1beta1.CronJob, error) {
	cronJob.Spec.Schedule = row.Schedule
	cronJob.Spec.TimeZone = ptr.To(defaultTimeZone(row.TimeZone))
	cronJob.Spec.Suspend = ptr.To(suspended(row))
	if cronJob.Annotations == nil {
		cronJob.Annotations = map[string]string{}
	}
	cronJob.Annotations[ManagedByAnnotation] = ManagedByScheduled
	if row.DisplayName != "" {
		cronJob.Annotations["onexai.io/display-name"] = row.DisplayName
	}
	if row.Description != "" {
		cronJob.Annotations["onexai.io/description"] = row.Description
	}
	if len(row.Labels) > 0 {
		cronJob.Labels = copyStringMap(row.Labels)
	}
	if err := setJobTemplate(cronJob, row); err != nil {
		return nil, err
	}
	return cronJob, nil
}

func setJobTemplate(cronJob *batchv1beta1.CronJob, row *model.ScheduledRunM) error {
	subject := row.Owner
	if subject == "" {
		subject = row.CreatedBy
	}
	if subject == "" {
		subject = "scheduled-run"
	}
	spec := batchv1beta1.AgentRunProviderSpec{
		Agent:            row.Agent,
		Prompt:           row.Prompt,
		SubjectID:        subject,
		SubjectName:      subject,
		Metering:         "none",
		ApprovalFallback: "deny",
		SessionPolicy:    "new",
		Source:           "scheduled-run",
	}
	if row.ExecutionTimeoutNanos > 0 {
		spec.TimeoutSeconds = ptr.To(row.ExecutionTimeoutNanos / int64(1e9))
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode agentrun providerSpec: %w", err)
	}

	cronJob.Spec.JobTemplate = batchv1beta1.JobTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      copyStringMap(row.Labels),
			Annotations: map[string]string{ManagedByAnnotation: ManagedByScheduled},
		},
		Spec: batchv1beta1.JobSpec{
			Type:                  batchv1beta1.JobTypeAgentRun,
			ActiveDeadlineSeconds: timeoutSeconds(row.ExecutionTimeoutNanos),
			BackoffLimit:          ptr.To[int32](0),
			ProviderSpec:          &runtime.RawExtension{Raw: raw},
		},
	}
	return nil
}

// nextScheduledAt computes the next firing time in Unix milliseconds. It
// mirrors the CronJob controller's parsing (including CRON_TZ) so the console
// shows the same instant the scheduler will use. A paused schedule, or one
// whose expression cannot be parsed, has no next run.
func nextScheduledAt(cronJob *batchv1beta1.CronJob, now time.Time) int64 {
	if cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend {
		return 0
	}

	spec := cronJob.Spec.Schedule
	if tz := ptr.Deref(cronJob.Spec.TimeZone, ""); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return 0
		}
		spec = "CRON_TZ=" + tz + " " + spec
	}

	schedule, err := cron.ParseStandard(spec)
	if err != nil {
		return 0
	}
	return schedule.Next(now).UnixMilli()
}

func rowFromCronJob(cronJob *batchv1beta1.CronJob) *model.ScheduledRunM {
	row := &model.ScheduledRunM{
		Name:        cronJob.Name,
		Namespace:   cronJob.Namespace,
		DisplayName: cronJob.Annotations["onexai.io/display-name"],
		Description: cronJob.Annotations["onexai.io/description"],
		Status:      "enabled",
		CreatedBy:   cronJob.Annotations[agentv1alpha1.AnnotationCreatedBy],
		Schedule:    cronJob.Spec.Schedule,
		Labels:      model.StringMap(copyStringMap(cronJob.Labels)),
		CreatedAt:   cronJob.CreationTimestamp.UnixMilli(),
		UpdatedAt:   cronJob.CreationTimestamp.UnixMilli(),
	}
	if cronJob.Spec.TimeZone != nil {
		row.TimeZone = *cronJob.Spec.TimeZone
	}
	if cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend {
		row.Paused = true
		row.Status = "disabled"
	}
	if cronJob.Status.LastScheduleTime != nil {
		row.LastScheduledAt = cronJob.Status.LastScheduleTime.UnixMilli()
	}
	row.NextScheduledAt = nextScheduledAt(cronJob, time.Now())
	if len(cronJob.Status.Active) > 0 {
		row.LastExecutionID = cronJob.Status.Active[len(cronJob.Status.Active)-1].Name
	}

	var spec batchv1beta1.AgentRunProviderSpec
	if err := decodeProviderSpec(&cronJob.Spec.JobTemplate.Spec, &spec); err == nil {
		row.Agent = spec.Agent
		row.Prompt = spec.Prompt
		row.Owner = spec.SubjectID
		if spec.TimeoutSeconds != nil {
			row.ExecutionTimeoutNanos = *spec.TimeoutSeconds * int64(1e9)
		}
	}
	if row.ExecutionTimeoutNanos == 0 && cronJob.Spec.JobTemplate.Spec.ActiveDeadlineSeconds != nil {
		row.ExecutionTimeoutNanos = *cronJob.Spec.JobTemplate.Spec.ActiveDeadlineSeconds * int64(1e9)
	}
	return row
}

func decodeProviderSpec(spec *batchv1beta1.JobSpec, out any) error {
	if spec.ProviderSpec == nil || len(spec.ProviderSpec.Raw) == 0 {
		return fmt.Errorf("no providerSpec")
	}
	return json.Unmarshal(spec.ProviderSpec.Raw, out)
}

func suspended(row *model.ScheduledRunM) bool {
	return row.Paused || row.Status == model.StatusDisabled
}

func timeoutSeconds(nanos int64) *int64 {
	if nanos <= 0 {
		return nil
	}
	return ptr.To(nanos / int64(1e9))
}

func defaultTimeZone(tz string) string {
	if tz == "" {
		return "UTC"
	}
	return tz
}

func rowNamespace(ns, fallback string) string {
	if ns != "" {
		return ns
	}
	if fallback != "" {
		return fallback
	}
	// The declarative API treats an empty namespace as the platform default;
	// a CronJob cannot be created cluster-wide, so creation needs a concrete
	// one. Listing across every namespace still uses "" (see filterNamespace).
	return model.DefaultNamespace
}

func filterString(opts *where.Options, key string) string {
	if opts == nil {
		return ""
	}
	if v, ok := opts.Filters[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func filterNamespace(opts *where.Options, fallback string) string {
	if ns := filterString(opts, "workspace"); ns != "" {
		return ns
	}
	if ns := filterString(opts, "namespace"); ns != "" {
		return ns
	}
	return fallback
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
