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

package clusteragentbinding

import (
	"context"
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"sigs.k8s.io/structured-merge-diff/v7/fieldpath"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
	statusvalidation "github.com/onexstack/onexai/pkg/apis/validation"
)

// clusterAgentBindingStrategy implements behavior for ClusterAgentBinding objects.
type clusterAgentBindingStrategy struct {
	runtime.ObjectTyper
	names.NameGenerator
}

// Strategy is the default logic that applies when creating and updating
// ClusterAgentBinding objects via the REST API.
var Strategy = clusterAgentBindingStrategy{legacyscheme.Scheme, names.SimpleNameGenerator}

var (
	_                         = rest.GarbageCollectionDeleteStrategy(Strategy)
	_ rest.RESTCreateStrategy = Strategy
	_ rest.RESTUpdateStrategy = Strategy
)

// DefaultGarbageCollectionPolicy returns DeleteDependents for all currently served versions.
func (clusterAgentBindingStrategy) DefaultGarbageCollectionPolicy(context.Context) rest.GarbageCollectionPolicy {
	return rest.DeleteDependents
}

// NamespaceScoped is false: the binding's audience spans namespaces, so the
// object cannot belong to one of them.
func (clusterAgentBindingStrategy) NamespaceScoped() bool { return false }

// GetResetFields returns the set of fields that get reset by the strategy.
func (clusterAgentBindingStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"agent.onexai.io/v1alpha1": fieldpath.NewSet(
			fieldpath.MakePathOrDie("status"),
		),
	}
}

// PrepareForCreate clears fields that are not allowed to be set by end users on
// creation, canonicalizes the lists and records who created the binding.
func (clusterAgentBindingStrategy) PrepareForCreate(ctx context.Context, obj runtime.Object) {
	r := obj.(*agent.ClusterAgentBinding)
	r.Status = agent.ClusterAgentBindingStatus{}
	r.Generation = 1
	agentvalidation.CanonicalizeClusterAgentBindingSpec(&r.Spec)
	recordCreatedBy(ctx, r)
}

// Validate validates a new cluster agent binding.
func (clusterAgentBindingStrategy) Validate(_ context.Context, obj runtime.Object) field.ErrorList {
	return agentvalidation.ValidateClusterAgentBinding(&obj.(*agent.ClusterAgentBinding).Spec, field.NewPath("spec"))
}

// WarningsOnCreate returns warnings for the creation of the given object.
func (clusterAgentBindingStrategy) WarningsOnCreate(context.Context, runtime.Object) []string {
	return nil
}

// Canonicalize normalizes the object after validation.
func (clusterAgentBindingStrategy) Canonicalize(runtime.Object) {}

// AllowCreateOnUpdate is false for cluster agent bindings.
func (clusterAgentBindingStrategy) AllowCreateOnUpdate(context.Context) bool { return false }

// PrepareForUpdate preserves status and the creator, and bumps the generation
// only when the spec actually changed.
func (clusterAgentBindingStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newR := obj.(*agent.ClusterAgentBinding)
	oldR := old.(*agent.ClusterAgentBinding)
	newR.Status = oldR.Status
	if oldR.Annotations[agentv1alpha1.AnnotationCreatedBy] != "" {
		if newR.Annotations == nil {
			newR.Annotations = map[string]string{}
		}
		newR.Annotations[agentv1alpha1.AnnotationCreatedBy] = oldR.Annotations[agentv1alpha1.AnnotationCreatedBy]
	}

	agentvalidation.CanonicalizeClusterAgentBindingSpec(&newR.Spec)
	if !apiequality.Semantic.DeepEqual(oldR.Spec, newR.Spec) {
		newR.Generation = oldR.Generation + 1
	}
}

// ValidateUpdate is the default update validation for an end user.
func (clusterAgentBindingStrategy) ValidateUpdate(_ context.Context, obj, _ runtime.Object) field.ErrorList {
	return agentvalidation.ValidateClusterAgentBinding(&obj.(*agent.ClusterAgentBinding).Spec, field.NewPath("spec"))
}

// WarningsOnUpdate returns warnings for the given update.
func (clusterAgentBindingStrategy) WarningsOnUpdate(context.Context, runtime.Object, runtime.Object) []string {
	return nil
}

// AllowUnconditionalUpdate returns false so updates must carry a resourceVersion.
func (clusterAgentBindingStrategy) AllowUnconditionalUpdate(context.Context) bool { return false }

// clusterAgentBindingStatusStrategy implements behavior for the status subresource.
type clusterAgentBindingStatusStrategy struct {
	clusterAgentBindingStrategy
}

// StatusStrategy is the default logic invoked when updating object status.
var StatusStrategy = clusterAgentBindingStatusStrategy{Strategy}

// GetResetFields returns the set of fields that get reset by the status strategy.
func (clusterAgentBindingStatusStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"agent.onexai.io/v1alpha1": fieldpath.NewSet(
			fieldpath.MakePathOrDie("spec"),
			fieldpath.MakePathOrDie("metadata", "labels"),
		),
	}
}

// PrepareForUpdate clears fields that are not allowed to be set on status update.
func (clusterAgentBindingStatusStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newR := obj.(*agent.ClusterAgentBinding)
	oldR := old.(*agent.ClusterAgentBinding)
	newR.Spec = oldR.Spec
	newR.DeletionTimestamp = oldR.DeletionTimestamp
	newR.OwnerReferences = oldR.OwnerReferences
	newR.Labels = oldR.Labels
}

// ValidateUpdate is the default update validation for a status update.
func (clusterAgentBindingStatusStrategy) ValidateUpdate(_ context.Context, obj, _ runtime.Object) field.ErrorList {
	r := obj.(*agent.ClusterAgentBinding)
	return statusvalidation.ValidateCommonStatus(r.Status.Conditions, r.Status.ObservedGeneration, field.NewPath("status"))
}

// WarningsOnUpdate returns warnings for the given status update.
func (clusterAgentBindingStatusStrategy) WarningsOnUpdate(context.Context, runtime.Object, runtime.Object) []string {
	return nil
}

// Canonicalize normalizes the object after validation.
func (clusterAgentBindingStatusStrategy) Canonicalize(runtime.Object) {}

// GetAttrs returns labels and fields of a given object for filtering purposes.
func GetAttrs(obj runtime.Object) (labels.Set, fields.Set, error) {
	r, ok := obj.(*agent.ClusterAgentBinding)
	if !ok {
		return nil, nil, fmt.Errorf("given object is not a cluster agent binding")
	}
	return labels.Set(r.Labels), ToSelectableFields(r), nil
}

// ToSelectableFields returns a field set that can be used for field selection.
func ToSelectableFields(obj *agent.ClusterAgentBinding) fields.Set {
	return generic.MergeFieldsSets(
		generic.ObjectMetaFieldsSet(&obj.ObjectMeta, false),
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

// recordCreatedBy stamps the authenticated creator. It is written once and
// preserved on update.
func recordCreatedBy(ctx context.Context, r *agent.ClusterAgentBinding) {
	user, ok := request.UserFrom(ctx)
	if !ok || user == nil || user.GetName() == "" {
		return
	}
	if r.Annotations == nil {
		r.Annotations = map[string]string{}
	}
	if _, exists := r.Annotations[agentv1alpha1.AnnotationCreatedBy]; exists {
		return
	}
	r.Annotations[agentv1alpha1.AnnotationCreatedBy] = user.GetName()
}
