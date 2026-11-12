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

package storage

import (
	"context"

	"github.com/onexstack/onexstack/pkg/ptr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/registry/generic"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/structured-merge-diff/v7/fieldpath"

	"github.com/onexstack/onexai/internal/apiserver/printers"
	clusteragentbindingregistry "github.com/onexstack/onexai/internal/apiserver/registry/agent/clusteragentbinding"
	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// ClusterAgentBindingStorage includes storage for clusteragentbindings and all
// subresources.
type ClusterAgentBindingStorage struct {
	ClusterAgentBinding *REST
	Status              *StatusREST
}

// NewStorage returns a new instance of ClusterAgentBindingStorage.
func NewStorage(optsGetter generic.RESTOptionsGetter) (ClusterAgentBindingStorage, error) {
	rest, statusRest, err := NewREST(optsGetter)
	if err != nil {
		return ClusterAgentBindingStorage{}, err
	}

	return ClusterAgentBindingStorage{ClusterAgentBinding: rest, Status: statusRest}, nil
}

// REST implements a RESTStorage for clusteragentbindings.
type REST struct {
	*genericregistry.Store
}

// Categories implements rest.CategoriesProvider so that both "kubectl get all"
// and "kubectl get onexai" list this resource alongside the rest of the API objects.
func (r *REST) Categories() []string { return []string{"all", "onexai"} }

// NewREST returns a RESTStorage object that will work against
// clusteragentbindings.
func NewREST(optsGetter generic.RESTOptionsGetter) (*REST, *StatusREST, error) {
	store := &genericregistry.Store{
		NewFunc:       func() runtime.Object { return &agent.ClusterAgentBinding{} },
		NewListFunc:   func() runtime.Object { return &agent.ClusterAgentBindingList{} },
		PredicateFunc: clusteragentbindingregistry.Matcher,
		ObjectNameFunc: func(obj runtime.Object) (string, error) {
			return obj.(*agent.ClusterAgentBinding).Name, nil
		},
		DefaultQualifiedResource:  agent.Resource("clusteragentbindings"),
		SingularQualifiedResource: agent.Resource("clusteragentbinding"),

		CreateStrategy:      clusteragentbindingregistry.Strategy,
		UpdateStrategy:      clusteragentbindingregistry.Strategy,
		DeleteStrategy:      clusteragentbindingregistry.Strategy,
		ResetFieldsStrategy: clusteragentbindingregistry.Strategy,

		TableConvertor: printers.ClusterAgentBindings(),
	}
	options := &generic.StoreOptions{RESTOptions: optsGetter, AttrFunc: clusteragentbindingregistry.GetAttrs}
	if err := store.CompleteWithOptions(options); err != nil {
		return nil, nil, err
	}

	statusStore := ptr.Clone(store)
	statusStore.UpdateStrategy = clusteragentbindingregistry.StatusStrategy
	statusStore.ResetFieldsStrategy = clusteragentbindingregistry.StatusStrategy
	return &REST{store}, &StatusREST{store: statusStore}, nil
}

// SetWriteAuthorizer implements storage.WriteAuthorizerSetter.
func (r *REST) SetWriteAuthorizer(a authorizer.Authorizer, recorder record.EventRecorder) {
	admission := clusteragentbindingregistry.Admission{Authorizer: a, Recorder: recorder}
	r.Store.BeginCreate = func(
		ctx context.Context, obj runtime.Object, _ *metav1.CreateOptions,
	) (genericregistry.FinishFunc, error) {
		if err := admission.Admit(ctx, obj.(*agent.ClusterAgentBinding)); err != nil {
			return nil, err
		}
		return func(context.Context, bool) {}, nil
	}
	r.Store.BeginUpdate = func(
		ctx context.Context, obj, _ runtime.Object, _ *metav1.UpdateOptions,
	) (genericregistry.FinishFunc, error) {
		if err := admission.Admit(ctx, obj.(*agent.ClusterAgentBinding)); err != nil {
			return nil, err
		}
		return func(context.Context, bool) {}, nil
	}
}

// StatusREST implements the REST endpoint for changing the status of a
// clusteragentbinding.
type StatusREST struct {
	store *genericregistry.Store
}

// New returns an empty ClusterAgentBinding object.
func (r *StatusREST) New() runtime.Object { return &agent.ClusterAgentBinding{} }

// Destroy cleans up resources on shutdown.
func (r *StatusREST) Destroy() {}

// Get retrieves the object from storage. It is required to support Patch.
func (r *StatusREST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	return r.store.Get(ctx, name, options)
}

// Update alters the status subset of an object.
func (r *StatusREST) Update(
	ctx context.Context,
	name string,
	objInfo rest.UpdatedObjectInfo,
	createValidation rest.ValidateObjectFunc,
	updateValidation rest.ValidateObjectUpdateFunc,
	forceAllowCreate bool,
	options *metav1.UpdateOptions,
) (runtime.Object, bool, error) {
	return r.store.Update(ctx, name, objInfo, createValidation, updateValidation, false, options)
}

// GetResetFields implements rest.ResetFieldsStrategy.
func (r *StatusREST) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return r.store.GetResetFields()
}

// ConvertToTable implements rest.TableConverter.
func (r *StatusREST) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return r.store.ConvertToTable(ctx, object, tableOptions)
}
