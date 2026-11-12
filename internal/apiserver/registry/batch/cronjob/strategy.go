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

// Package cronjob implements the REST strategy for batch CronJobs.
package cronjob

import (
	"context"
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/registry/rest"
	apiserverstorage "k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"sigs.k8s.io/structured-merge-diff/v7/fieldpath"

	"github.com/onexstack/onexai/pkg/apis/batch"
	"github.com/onexstack/onexai/pkg/apis/batch/validation"
)

type cronJobStrategy struct {
	runtime.ObjectTyper
	names.NameGenerator
}

// Strategy is the default logic that applies when creating and updating CronJobs.
var Strategy = cronJobStrategy{legacyscheme.Scheme, names.SimpleNameGenerator}

var (
	_                         = rest.GarbageCollectionDeleteStrategy(Strategy)
	_ rest.RESTCreateStrategy = Strategy
	_ rest.RESTUpdateStrategy = Strategy
)

// DefaultGarbageCollectionPolicy returns DeleteDependents so deleting a CronJob
// also deletes the Jobs it owns.
func (cronJobStrategy) DefaultGarbageCollectionPolicy(_ context.Context) rest.GarbageCollectionPolicy {
	return rest.DeleteDependents
}

// NamespaceScoped is true for cronjobs.
func (cronJobStrategy) NamespaceScoped() bool { return true }

// GetResetFields returns the fields the strategy resets.
func (cronJobStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"batch.onex.io/v1beta1": fieldpath.NewSet(fieldpath.MakePathOrDie("status")),
	}
}

// PrepareForCreate clears status and stamps the first generation.
func (cronJobStrategy) PrepareForCreate(_ context.Context, obj runtime.Object) {
	c := obj.(*batch.CronJob)
	c.Status = batch.CronJobStatus{}
	c.Generation = 1
}

// Validate validates a new cronjob.
func (cronJobStrategy) Validate(_ context.Context, obj runtime.Object) field.ErrorList {
	return validation.ValidateCronJobCreate(obj.(*batch.CronJob))
}

// WarningsOnCreate returns warnings for creation.
func (cronJobStrategy) WarningsOnCreate(_ context.Context, _ runtime.Object) []string { return nil }

// Canonicalize normalizes the object after validation.
func (cronJobStrategy) Canonicalize(_ runtime.Object) {}

// AllowCreateOnUpdate is false for cronjobs.
func (cronJobStrategy) AllowCreateOnUpdate(_ context.Context) bool { return false }

// PrepareForUpdate preserves status and bumps generation on spec changes.
func (cronJobStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newCronJob := obj.(*batch.CronJob)
	oldCronJob := old.(*batch.CronJob)
	newCronJob.Status = oldCronJob.Status
	if !apiequality.Semantic.DeepEqual(oldCronJob.Spec, newCronJob.Spec) {
		newCronJob.Generation = oldCronJob.Generation + 1
	}
}

// ValidateUpdate validates an update.
func (cronJobStrategy) ValidateUpdate(_ context.Context, obj, old runtime.Object) field.ErrorList {
	return validation.ValidateCronJobUpdate(obj.(*batch.CronJob), old.(*batch.CronJob))
}

// WarningsOnUpdate returns warnings for updates.
func (cronJobStrategy) WarningsOnUpdate(_ context.Context, _, _ runtime.Object) []string {
	return nil
}

// AllowUnconditionalUpdate requires a resourceVersion on update.
func (cronJobStrategy) AllowUnconditionalUpdate(_ context.Context) bool { return false }

type cronJobStatusStrategy struct{ cronJobStrategy }

// StatusStrategy is the strategy for the status subresource.
var StatusStrategy = cronJobStatusStrategy{Strategy}

// GetResetFields returns the fields the status strategy resets.
func (cronJobStatusStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"batch.onex.io/v1beta1": fieldpath.NewSet(
			fieldpath.MakePathOrDie("spec"),
			fieldpath.MakePathOrDie("metadata", "labels"),
		),
	}
}

// PrepareForUpdate only lets status change.
func (cronJobStatusStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newCronJob := obj.(*batch.CronJob)
	oldCronJob := old.(*batch.CronJob)
	newCronJob.Spec = oldCronJob.Spec
	newCronJob.Labels = oldCronJob.Labels
	newCronJob.DeletionTimestamp = nil
	newCronJob.OwnerReferences = oldCronJob.OwnerReferences
}

// ValidateUpdate validates a status update.
func (cronJobStatusStrategy) ValidateUpdate(_ context.Context, obj, old runtime.Object) field.ErrorList {
	return validation.ValidateCronJobUpdate(obj.(*batch.CronJob), old.(*batch.CronJob))
}

// WarningsOnUpdate returns warnings for status updates.
func (cronJobStatusStrategy) WarningsOnUpdate(_ context.Context, _, _ runtime.Object) []string {
	return nil
}

// Canonicalize normalizes the object after validation.
func (cronJobStatusStrategy) Canonicalize(_ runtime.Object) {}

// ToSelectableFields returns the fields a CronJob can be selected by.
func ToSelectableFields(obj *batch.CronJob) fields.Set {
	return generic.ObjectMetaFieldsSet(&obj.ObjectMeta, true)
}

// GetAttrs returns labels and fields for filtering.
func GetAttrs(obj runtime.Object) (labels.Set, fields.Set, error) {
	c, ok := obj.(*batch.CronJob)
	if !ok {
		return nil, nil, fmt.Errorf("given object is not a cronjob")
	}
	return labels.Set(c.Labels), ToSelectableFields(c), nil
}

// Matcher is the filter used by the generic etcd backend.
func Matcher(label labels.Selector, fieldSelector fields.Selector) apiserverstorage.SelectionPredicate {
	return apiserverstorage.SelectionPredicate{
		Label:    label,
		Field:    fieldSelector,
		GetAttrs: GetAttrs,
	}
}
