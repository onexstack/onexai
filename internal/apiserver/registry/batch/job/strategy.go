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

// Package job implements the REST strategy for batch Jobs.
package job

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

type jobStrategy struct {
	runtime.ObjectTyper
	names.NameGenerator
}

// Strategy is the default logic that applies when creating and updating Jobs.
var Strategy = jobStrategy{legacyscheme.Scheme, names.SimpleNameGenerator}

var (
	_                         = rest.GarbageCollectionDeleteStrategy(Strategy)
	_ rest.RESTCreateStrategy = Strategy
	_ rest.RESTUpdateStrategy = Strategy
)

// DefaultGarbageCollectionPolicy returns DeleteDependents so a deleted CronJob
// takes its child Jobs with it.
func (jobStrategy) DefaultGarbageCollectionPolicy(_ context.Context) rest.GarbageCollectionPolicy {
	return rest.DeleteDependents
}

// NamespaceScoped is true for jobs.
func (jobStrategy) NamespaceScoped() bool { return true }

// GetResetFields returns the fields the strategy resets.
func (jobStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"batch.onex.io/v1beta1": fieldpath.NewSet(fieldpath.MakePathOrDie("status")),
	}
}

// PrepareForCreate clears status and stamps the first generation.
func (jobStrategy) PrepareForCreate(_ context.Context, obj runtime.Object) {
	j := obj.(*batch.Job)
	j.Status = batch.JobStatus{}
	j.Generation = 1
}

// Validate validates a new job.
func (jobStrategy) Validate(_ context.Context, obj runtime.Object) field.ErrorList {
	return validation.ValidateJob(obj.(*batch.Job))
}

// WarningsOnCreate returns warnings for creation.
func (jobStrategy) WarningsOnCreate(_ context.Context, _ runtime.Object) []string { return nil }

// Canonicalize normalizes the object after validation.
func (jobStrategy) Canonicalize(_ runtime.Object) {}

// AllowCreateOnUpdate is false for jobs.
func (jobStrategy) AllowCreateOnUpdate(_ context.Context) bool { return false }

// PrepareForUpdate preserves status and bumps generation on spec changes.
func (jobStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newJob := obj.(*batch.Job)
	oldJob := old.(*batch.Job)
	newJob.Status = oldJob.Status
	if !apiequality.Semantic.DeepEqual(oldJob.Spec, newJob.Spec) {
		newJob.Generation = oldJob.Generation + 1
	}
}

// ValidateUpdate validates an update.
func (jobStrategy) ValidateUpdate(_ context.Context, obj, old runtime.Object) field.ErrorList {
	return validation.ValidateJobUpdate(obj.(*batch.Job), old.(*batch.Job))
}

// WarningsOnUpdate returns warnings for updates.
func (jobStrategy) WarningsOnUpdate(_ context.Context, _, _ runtime.Object) []string { return nil }

// AllowUnconditionalUpdate requires a resourceVersion on update.
func (jobStrategy) AllowUnconditionalUpdate(_ context.Context) bool { return false }

type jobStatusStrategy struct{ jobStrategy }

// StatusStrategy is the strategy for the status subresource.
var StatusStrategy = jobStatusStrategy{Strategy}

// GetResetFields returns the fields the status strategy resets.
func (jobStatusStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"batch.onex.io/v1beta1": fieldpath.NewSet(
			fieldpath.MakePathOrDie("spec"),
			fieldpath.MakePathOrDie("metadata", "labels"),
		),
	}
}

// PrepareForUpdate only lets status change.
func (jobStatusStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newJob := obj.(*batch.Job)
	oldJob := old.(*batch.Job)
	newJob.Spec = oldJob.Spec
	newJob.DeletionTimestamp = nil
	newJob.OwnerReferences = oldJob.OwnerReferences
}

// ValidateUpdate validates a status update.
func (jobStatusStrategy) ValidateUpdate(_ context.Context, obj, old runtime.Object) field.ErrorList {
	return validation.ValidateJobStatusUpdate(obj.(*batch.Job), old.(*batch.Job))
}

// WarningsOnUpdate returns warnings for status updates.
func (jobStatusStrategy) WarningsOnUpdate(_ context.Context, _, _ runtime.Object) []string {
	return nil
}

// Canonicalize normalizes the object after validation.
func (jobStatusStrategy) Canonicalize(_ runtime.Object) {}

// ToSelectableFields returns the fields a Job can be selected by.
func ToSelectableFields(obj *batch.Job) fields.Set {
	return generic.ObjectMetaFieldsSet(&obj.ObjectMeta, true)
}

// GetAttrs returns labels and fields for filtering.
func GetAttrs(obj runtime.Object) (labels.Set, fields.Set, error) {
	j, ok := obj.(*batch.Job)
	if !ok {
		return nil, nil, fmt.Errorf("given object is not a job")
	}
	return labels.Set(j.Labels), ToSelectableFields(j), nil
}

// Matcher is the filter used by the generic etcd backend.
func Matcher(label labels.Selector, fieldSelector fields.Selector) apiserverstorage.SelectionPredicate {
	return apiserverstorage.SelectionPredicate{
		Label:    label,
		Field:    fieldSelector,
		GetAttrs: GetAttrs,
	}
}
