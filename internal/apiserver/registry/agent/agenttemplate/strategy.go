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

package agenttemplate

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
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"sigs.k8s.io/structured-merge-diff/v7/fieldpath"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
	statusvalidation "github.com/onexstack/onexai/pkg/apis/validation"
)

// agentTemplateStrategy implements behavior for AgentTemplate objects.
type agentTemplateStrategy struct {
	runtime.ObjectTyper
	names.NameGenerator
}

// Strategy is the default logic that applies when creating and updating
// AgentTemplate objects via the REST API.
var Strategy = agentTemplateStrategy{legacyscheme.Scheme, names.SimpleNameGenerator}

var (
	_                         = rest.GarbageCollectionDeleteStrategy(Strategy)
	_ rest.RESTCreateStrategy = Strategy
	_ rest.RESTUpdateStrategy = Strategy
)

// DefaultGarbageCollectionPolicy returns DeleteDependents for all currently served versions.
func (agentTemplateStrategy) DefaultGarbageCollectionPolicy(_ context.Context) rest.GarbageCollectionPolicy {
	return rest.DeleteDependents
}

// NamespaceScoped is true for agenttemplates.
func (agentTemplateStrategy) NamespaceScoped() bool { return true }

// GetResetFields returns the set of fields that get reset by the strategy.
func (agentTemplateStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"agent.onexai.io/v1alpha1": fieldpath.NewSet(
			fieldpath.MakePathOrDie("status"),
		),
	}
}

// PrepareForCreate clears fields that are not allowed to be set by end users on creation.
func (agentTemplateStrategy) PrepareForCreate(_ context.Context, obj runtime.Object) {
	r := obj.(*agent.AgentTemplate)
	r.Status = agent.AgentTemplateStatus{}
	r.Generation = 1
}

// Validate validates a new template.
func (agentTemplateStrategy) Validate(_ context.Context, obj runtime.Object) field.ErrorList {
	return agentvalidation.ValidateAgentTemplate(&obj.(*agent.AgentTemplate).Spec, field.NewPath("spec"))
}

// WarningsOnCreate returns warnings for the creation of the given object.
func (agentTemplateStrategy) WarningsOnCreate(_ context.Context, _ runtime.Object) []string {
	return nil
}

// Canonicalize normalizes the object after validation.
func (agentTemplateStrategy) Canonicalize(_ runtime.Object) {}

// AllowCreateOnUpdate is false for agenttemplates.
func (agentTemplateStrategy) AllowCreateOnUpdate(_ context.Context) bool { return false }

// PrepareForUpdate clears fields that are not allowed to be set by end users on update.
func (agentTemplateStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newR := obj.(*agent.AgentTemplate)
	oldR := old.(*agent.AgentTemplate)
	newR.Status = oldR.Status

	if !apiequality.Semantic.DeepEqual(oldR.Spec, newR.Spec) {
		newR.Generation = oldR.Generation + 1
	}
}

// ValidateUpdate is the default update validation for an end user.
func (agentTemplateStrategy) ValidateUpdate(_ context.Context, obj, _ runtime.Object) field.ErrorList {
	return agentvalidation.ValidateAgentTemplate(&obj.(*agent.AgentTemplate).Spec, field.NewPath("spec"))
}

// WarningsOnUpdate returns warnings for the given update.
func (agentTemplateStrategy) WarningsOnUpdate(_ context.Context, _, _ runtime.Object) []string {
	return nil
}

// AllowUnconditionalUpdate returns false so updates must carry a resourceVersion.
func (agentTemplateStrategy) AllowUnconditionalUpdate(_ context.Context) bool { return false }

// agentTemplateStatusStrategy implements behavior for the status subresource.
type agentTemplateStatusStrategy struct {
	agentTemplateStrategy
}

// StatusStrategy is the default logic invoked when updating object status.
var StatusStrategy = agentTemplateStatusStrategy{Strategy}

// GetResetFields returns the set of fields that get reset by the status strategy.
func (agentTemplateStatusStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"agent.onexai.io/v1alpha1": fieldpath.NewSet(
			fieldpath.MakePathOrDie("spec"),
			fieldpath.MakePathOrDie("metadata", "labels"),
		),
	}
}

// PrepareForUpdate clears fields that are not allowed to be set on status update.
func (agentTemplateStatusStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newR := obj.(*agent.AgentTemplate)
	oldR := old.(*agent.AgentTemplate)
	newR.Spec = oldR.Spec
	newR.DeletionTimestamp = oldR.DeletionTimestamp
	newR.OwnerReferences = oldR.OwnerReferences
	newR.Labels = oldR.Labels
}

// ValidateUpdate is the default update validation for a status update.
func (agentTemplateStatusStrategy) ValidateUpdate(_ context.Context, obj, _ runtime.Object) field.ErrorList {
	r := obj.(*agent.AgentTemplate)
	return statusvalidation.ValidateCommonStatus(r.Status.Conditions, r.Status.ObservedGeneration, field.NewPath("status"))
}

// WarningsOnUpdate returns warnings for the given status update.
func (agentTemplateStatusStrategy) WarningsOnUpdate(_ context.Context, _, _ runtime.Object) []string {
	return nil
}

// Canonicalize normalizes the object after validation.
func (agentTemplateStatusStrategy) Canonicalize(_ runtime.Object) {}

// GetAttrs returns labels and fields of a given object for filtering purposes.
func GetAttrs(obj runtime.Object) (labels.Set, fields.Set, error) {
	r, ok := obj.(*agent.AgentTemplate)
	if !ok {
		return nil, nil, fmt.Errorf("given object is not an agenttemplate")
	}
	return labels.Set(r.Labels), ToSelectableFields(r), nil
}

// ToSelectableFields returns a field set that can be used for field selection.
func ToSelectableFields(obj *agent.AgentTemplate) fields.Set {
	return generic.MergeFieldsSets(
		generic.ObjectMetaFieldsSet(&obj.ObjectMeta, true),
		fields.Set{},
	)
}

// Matcher is the filter used by the generic etcd backend.
func Matcher(label labels.Selector, field fields.Selector) storage.SelectionPredicate {
	return storage.SelectionPredicate{
		Label:    label,
		Field:    field,
		GetAttrs: GetAttrs,
	}
}
