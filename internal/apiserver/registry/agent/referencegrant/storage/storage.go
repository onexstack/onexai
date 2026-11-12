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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/generic"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"

	"github.com/onexstack/onexai/internal/apiserver/printers"
	referencegrantregistry "github.com/onexstack/onexai/internal/apiserver/registry/agent/referencegrant"
	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

// ReferenceGrantStorage includes storage for referencegrants.
type ReferenceGrantStorage struct {
	ReferenceGrant *REST
}

// NewStorage returns a new instance of ReferenceGrantStorage.
func NewStorage(optsGetter generic.RESTOptionsGetter) (ReferenceGrantStorage, error) {
	rest, err := NewREST(optsGetter)
	if err != nil {
		return ReferenceGrantStorage{}, err
	}

	return ReferenceGrantStorage{ReferenceGrant: rest}, nil
}

// REST implements a RESTStorage for referencegrants.
type REST struct {
	*genericregistry.Store
}

// Categories implements rest.CategoriesProvider so that both "kubectl get all"
// and "kubectl get onexai" list this resource alongside the rest of the API objects.
func (r *REST) Categories() []string { return []string{"all", "onexai"} }

// NewREST returns a RESTStorage object that will work against referencegrants.
func NewREST(optsGetter generic.RESTOptionsGetter) (*REST, error) {
	store := &genericregistry.Store{
		NewFunc:       func() runtime.Object { return &agent.ReferenceGrant{} },
		NewListFunc:   func() runtime.Object { return &agent.ReferenceGrantList{} },
		PredicateFunc: referencegrantregistry.Matcher,
		ObjectNameFunc: func(obj runtime.Object) (string, error) {
			return obj.(*agent.ReferenceGrant).Name, nil
		},
		DefaultQualifiedResource:  agent.Resource("referencegrants"),
		SingularQualifiedResource: agent.Resource("referencegrant"),

		CreateStrategy: referencegrantregistry.Strategy,
		UpdateStrategy: referencegrantregistry.Strategy,
		DeleteStrategy: referencegrantregistry.Strategy,

		// The generic store refuses to start without a table converter, and the
		// convertor is what decides the columns `kubectl get` prints.
		TableConvertor: printers.ReferenceGrants(),
	}
	options := &generic.StoreOptions{RESTOptions: optsGetter, AttrFunc: referencegrantregistry.GetAttrs}
	if err := store.CompleteWithOptions(options); err != nil {
		return nil, err
	}
	return &REST{store}, nil
}
