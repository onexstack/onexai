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

package referencegrant

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
)

// referenceGrantStrategy implements behavior for ReferenceGrant objects.
type referenceGrantStrategy struct {
	runtime.ObjectTyper
	names.NameGenerator
}

// Strategy is the default logic that applies when creating and updating
// ReferenceGrant objects via the REST API.
var Strategy = referenceGrantStrategy{legacyscheme.Scheme, names.SimpleNameGenerator}

var (
	_                         = rest.GarbageCollectionDeleteStrategy(Strategy)
	_ rest.RESTCreateStrategy = Strategy
	_ rest.RESTUpdateStrategy = Strategy
)

// DefaultGarbageCollectionPolicy returns DeleteDependents for all currently
// served versions.
func (referenceGrantStrategy) DefaultGarbageCollectionPolicy(context.Context) rest.GarbageCollectionPolicy {
	return rest.DeleteDependents
}

// NamespaceScoped is true for grants: the grant lives in the target namespace.
func (referenceGrantStrategy) NamespaceScoped() bool { return true }

// GetResetFields returns no fields: a grant has no status subresource.
func (referenceGrantStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return nil
}

// PrepareForCreate sets the generation an object starts at.
func (referenceGrantStrategy) PrepareForCreate(_ context.Context, obj runtime.Object) {
	obj.(*agent.ReferenceGrant).Generation = 1
}

// Validate validates a new grant.
func (referenceGrantStrategy) Validate(_ context.Context, obj runtime.Object) field.ErrorList {
	return agentvalidation.ValidateReferenceGrant(&obj.(*agent.ReferenceGrant).Spec, field.NewPath("spec"))
}

// WarningsOnCreate returns no warnings for the creation of a grant.
func (referenceGrantStrategy) WarningsOnCreate(context.Context, runtime.Object) []string { return nil }

// Canonicalize normalizes the object after validation.
func (referenceGrantStrategy) Canonicalize(runtime.Object) {}

// AllowCreateOnUpdate is false for grants.
func (referenceGrantStrategy) AllowCreateOnUpdate(context.Context) bool { return false }

// PrepareForUpdate bumps the generation when the spec changes.
func (referenceGrantStrategy) PrepareForUpdate(_ context.Context, obj, old runtime.Object) {
	newR := obj.(*agent.ReferenceGrant)
	oldR := old.(*agent.ReferenceGrant)
	// A grant has no status, so nothing has to be carried over. The generation
	// is still bumped so a watcher can tell a spec change from a metadata-only
	// edit.
	if !apiequality.Semantic.DeepEqual(oldR.Spec, newR.Spec) {
		newR.Generation = oldR.Generation + 1
	}
}

// ValidateUpdate validates an update to a grant.
func (referenceGrantStrategy) ValidateUpdate(_ context.Context, obj, _ runtime.Object) field.ErrorList {
	return agentvalidation.ValidateReferenceGrant(&obj.(*agent.ReferenceGrant).Spec, field.NewPath("spec"))
}

// WarningsOnUpdate returns no warnings for the update of a grant.
func (referenceGrantStrategy) WarningsOnUpdate(_ context.Context, _, _ runtime.Object) []string {
	return nil
}

// AllowUnconditionalUpdate returns false so updates must carry a resourceVersion.
func (referenceGrantStrategy) AllowUnconditionalUpdate(context.Context) bool { return false }

// GetAttrs returns labels and fields of a given object for filtering purposes.
func GetAttrs(obj runtime.Object) (labels.Set, fields.Set, error) {
	r, ok := obj.(*agent.ReferenceGrant)
	if !ok {
		return nil, nil, fmt.Errorf("given object is not a referencegrant")
	}
	return labels.Set(r.Labels), ToSelectableFields(r), nil
}

// ToSelectableFields returns a field set that can be used for field selection.
func ToSelectableFields(obj *agent.ReferenceGrant) fields.Set {
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
