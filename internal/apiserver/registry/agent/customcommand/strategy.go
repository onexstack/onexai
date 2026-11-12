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

package customcommand

import (
	"context"
	"fmt"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"sigs.k8s.io/structured-merge-diff/v7/fieldpath"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
	statusvalidation "github.com/onexstack/onexai/pkg/apis/validation"
)

// customCommandStrategy implements behavior for CustomCommand objects.
type customCommandStrategy struct {
	runtime.ObjectTyper
	names.NameGenerator
}

// Strategy is the default logic that applies when creating and updating
// CustomCommand objects via the REST API.
var Strategy = customCommandStrategy{legacyscheme.Scheme, names.SimpleNameGenerator}

var (
	_                         = rest.GarbageCollectionDeleteStrategy(Strategy)
	_ rest.RESTCreateStrategy = Strategy
	_ rest.RESTUpdateStrategy = Strategy
)

// DefaultGarbageCollectionPolicy returns DeleteDependents for all currently served versions.
func (customCommandStrategy) DefaultGarbageCollectionPolicy(_ context.Context) rest.GarbageCollectionPolicy {
	return rest.DeleteDependents
}

// NamespaceScoped is true for custom commands.
func (customCommandStrategy) NamespaceScoped() bool {
	return true
}

// GetResetFields returns the set of fields that get reset by the strategy.
func (customCommandStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"agent.onexai.io/v1alpha1": fieldpath.NewSet(
			fieldpath.MakePathOrDie("status"),
		),
	}
}

// PrepareForCreate clears fields that are not allowed to be set by end users on creation.
func (customCommandStrategy) PrepareForCreate(_ context.Context, obj runtime.Object) {
	r := obj.(*agent.CustomCommand)
	r.Status = agent.CustomCommandStatus{}
	r.Generation = 1
}

// Validate validates a new custom command.
func (customCommandStrategy) Validate(_ context.Context, obj runtime.Object) field.ErrorList {
	r := obj.(*agent.CustomCommand)
	allErrs := agentvalidation.ValidateCustomCommandName(r.Name, field.NewPath("metadata", "name"))
	return append(allErrs, agentvalidation.ValidateCustomCommand(&r.Spec, field.NewPath("spec"))...)
}

// WarningsOnCreate returns warnings for the creation of the given object.
func (customCommandStrategy) WarningsOnCreate(_ context.Context, obj runtime.Object) []string {
	return warningsFor(obj)
}

// Canonicalize normalizes the object after validation.
func (customCommandStrategy) Canonicalize(_ runtime.Object) {}

// AllowCreateOnUpdate is false for custom commands.
func (customCommandStrategy) AllowCreateOnUpdate(_ context.Context) bool { return false }

// PrepareForUpdate clears fields that are not allowed to be set by end users on update.
func (customCommandStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newR := obj.(*agent.CustomCommand)
	oldR := old.(*agent.CustomCommand)
	newR.Status = oldR.Status

	if !apiequality.Semantic.DeepEqual(oldR.Spec, newR.Spec) {
		newR.Generation = oldR.Generation + 1
	}
}

// ValidateUpdate is the default update validation for an end user.
func (customCommandStrategy) ValidateUpdate(_ context.Context, obj, _ runtime.Object) field.ErrorList {
	r := obj.(*agent.CustomCommand)
	allErrs := agentvalidation.ValidateCustomCommandName(r.Name, field.NewPath("metadata", "name"))
	return append(allErrs, agentvalidation.ValidateCustomCommand(&r.Spec, field.NewPath("spec"))...)
}

// WarningsOnUpdate returns warnings for the given update.
func (customCommandStrategy) WarningsOnUpdate(_ context.Context, _, newObj runtime.Object) []string {
	return warningsFor(newObj)
}

// warningsFor reports configuration that is legal but worth saying out loud:
// the API server returns these as Warning headers on the response, so the person
// writing the command sees them once, at write time, rather than in an incident.
func warningsFor(obj runtime.Object) []string {
	r, ok := obj.(*agent.CustomCommand)
	if !ok || r.Spec.HTTP == nil {
		return nil
	}
	var warnings []string
	if r.Spec.HTTP.InsecureSkipTLSVerify {
		warnings = append(warnings,
			"spec.http.insecureSkipTLSVerify disables certificate verification for this command")
	}
	if r.Spec.HTTP.Service == nil && strings.HasPrefix(r.Spec.HTTP.URL, "http://") {
		warnings = append(warnings,
			"spec.http.url uses plain http; the request and every header it carries are not confidential")
	}
	return warnings
}

// AllowUnconditionalUpdate returns false so updates must carry a resourceVersion.
func (customCommandStrategy) AllowUnconditionalUpdate(_ context.Context) bool { return false }

// customCommandStatusStrategy implements behavior for the status subresource.
type customCommandStatusStrategy struct {
	customCommandStrategy
}

// StatusStrategy is the default logic invoked when updating object status.
var StatusStrategy = customCommandStatusStrategy{Strategy}

// GetResetFields returns the set of fields that get reset by the status strategy.
func (customCommandStatusStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"agent.onexai.io/v1alpha1": fieldpath.NewSet(
			fieldpath.MakePathOrDie("spec"),
			fieldpath.MakePathOrDie("metadata", "labels"),
		),
	}
}

// PrepareForUpdate clears fields that are not allowed to be set on status update.
func (customCommandStatusStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newR := obj.(*agent.CustomCommand)
	oldR := old.(*agent.CustomCommand)
	newR.Spec = oldR.Spec
	// A /status write must not cancel a pending deletion, reparent the object
	// (which breaks garbage collection), or relabel it. Copying the stored
	// metadata back makes the update unable to change any of them.
	newR.DeletionTimestamp = oldR.DeletionTimestamp
	newR.OwnerReferences = oldR.OwnerReferences
	newR.Labels = oldR.Labels
}

// ValidateUpdate is the default update validation for a status update.
func (customCommandStatusStrategy) ValidateUpdate(_ context.Context, obj, _ runtime.Object) field.ErrorList {
	r := obj.(*agent.CustomCommand)
	return statusvalidation.ValidateCommonStatus(r.Status.Conditions, r.Status.ObservedGeneration, field.NewPath("status"))
}

// WarningsOnUpdate returns warnings for the given status update.
func (customCommandStatusStrategy) WarningsOnUpdate(_ context.Context, _, _ runtime.Object) []string {
	return nil
}

// Canonicalize normalizes the object after validation.
func (customCommandStatusStrategy) Canonicalize(_ runtime.Object) {}

// GetAttrs returns labels and fields of a given object for filtering purposes.
func GetAttrs(obj runtime.Object) (labels.Set, fields.Set, error) {
	r, ok := obj.(*agent.CustomCommand)
	if !ok {
		return nil, nil, fmt.Errorf("given object is not a custom command")
	}
	return labels.Set(r.Labels), ToSelectableFields(r), nil
}

// ToSelectableFields returns a field set that can be used for field selection.
func ToSelectableFields(obj *agent.CustomCommand) fields.Set {
	return generic.MergeFieldsSets(
		generic.ObjectMetaFieldsSet(&obj.ObjectMeta, true),
		fields.Set{},
	)
}

// Matcher is the filter used by the generic etcd backend.
func Matcher(label labels.Selector, fieldSelector fields.Selector) storage.SelectionPredicate {
	return storage.SelectionPredicate{
		Label:    label,
		Field:    fieldSelector,
		GetAttrs: GetAttrs,
	}
}
