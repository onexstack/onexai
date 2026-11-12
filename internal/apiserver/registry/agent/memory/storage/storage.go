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
	"k8s.io/apiserver/pkg/registry/generic"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/structured-merge-diff/v7/fieldpath"

	"github.com/onexstack/onexai/internal/apiserver/printers"
	memoryregistry "github.com/onexstack/onexai/internal/apiserver/registry/agent/memory"
	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// MemoryStorage includes storage for memories and all subresources.
type MemoryStorage struct {
	Memory *REST
	Status *StatusREST
}

// NewStorage returns a new instance of MemoryStorage.
func NewStorage(optsGetter generic.RESTOptionsGetter) (MemoryStorage, error) {
	rest, statusRest, err := NewREST(optsGetter)
	if err != nil {
		return MemoryStorage{}, err
	}

	return MemoryStorage{Memory: rest, Status: statusRest}, nil
}

// REST implements a RESTStorage for memories.
type REST struct {
	*genericregistry.Store
}

// Categories implements rest.CategoriesProvider.
func (r *REST) Categories() []string { return []string{"all", "onexai"} }

// NewREST returns a RESTStorage object that will work against memories.
func NewREST(optsGetter generic.RESTOptionsGetter) (*REST, *StatusREST, error) {
	store := &genericregistry.Store{
		NewFunc:       func() runtime.Object { return &agent.Memory{} },
		NewListFunc:   func() runtime.Object { return &agent.MemoryList{} },
		PredicateFunc: memoryregistry.Matcher,
		ObjectNameFunc: func(obj runtime.Object) (string, error) {
			return obj.(*agent.Memory).Name, nil
		},
		DefaultQualifiedResource:  agent.Resource("memories"),
		SingularQualifiedResource: agent.Resource("memory"),

		CreateStrategy:      memoryregistry.Strategy,
		UpdateStrategy:      memoryregistry.Strategy,
		DeleteStrategy:      memoryregistry.Strategy,
		ResetFieldsStrategy: memoryregistry.Strategy,

		TableConvertor: printers.Memories(),
	}
	options := &generic.StoreOptions{RESTOptions: optsGetter, AttrFunc: memoryregistry.GetAttrs}
	if err := store.CompleteWithOptions(options); err != nil {
		return nil, nil, err
	}

	statusStore := ptr.Clone(store)
	statusStore.UpdateStrategy = memoryregistry.StatusStrategy
	statusStore.ResetFieldsStrategy = memoryregistry.StatusStrategy
	return &REST{store}, &StatusREST{store: statusStore}, nil
}

// StatusREST implements the REST endpoint for changing the status of a memory.
type StatusREST struct {
	store *genericregistry.Store
}

// New returns an empty Memory object.
func (r *StatusREST) New() runtime.Object { return &agent.Memory{} }

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
func (r *StatusREST) ConvertToTable(
	ctx context.Context, object runtime.Object, tableOptions runtime.Object,
) (*metav1.Table, error) {
	return r.store.ConvertToTable(ctx, object, tableOptions)
}
