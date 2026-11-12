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

// Package storage wires batch Jobs into the generic etcd registry.
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

	jobregistry "github.com/onexstack/onexai/internal/apiserver/registry/batch/job"
	"github.com/onexstack/onexai/pkg/apis/batch"
)

// JobStorage includes storage for jobs and their status subresource.
type JobStorage struct {
	Job    *REST
	Status *StatusREST
}

// NewStorage returns a new JobStorage.
func NewStorage(optsGetter generic.RESTOptionsGetter) (JobStorage, error) {
	jobRest, jobStatusRest, err := NewREST(optsGetter)
	if err != nil {
		return JobStorage{}, err
	}
	return JobStorage{Job: jobRest, Status: jobStatusRest}, nil
}

// REST implements a RESTStorage for jobs.
type REST struct{ *genericregistry.Store }

// Categories implements rest.CategoriesProvider.
func (r *REST) Categories() []string { return []string{"all", "onexai"} }

// NewREST returns a RESTStorage object that works against jobs.
func NewREST(optsGetter generic.RESTOptionsGetter) (*REST, *StatusREST, error) {
	store := &genericregistry.Store{
		NewFunc:       func() runtime.Object { return &batch.Job{} },
		NewListFunc:   func() runtime.Object { return &batch.JobList{} },
		PredicateFunc: jobregistry.Matcher,
		ObjectNameFunc: func(obj runtime.Object) (string, error) {
			return obj.(*batch.Job).Name, nil
		},
		DefaultQualifiedResource:  batch.Resource("jobs"),
		SingularQualifiedResource: batch.Resource("job"),

		CreateStrategy:      jobregistry.Strategy,
		UpdateStrategy:      jobregistry.Strategy,
		DeleteStrategy:      jobregistry.Strategy,
		ResetFieldsStrategy: jobregistry.Strategy,

		TableConvertor: rest.NewDefaultTableConvertor(batch.Resource("jobs")),
	}
	options := &generic.StoreOptions{RESTOptions: optsGetter, AttrFunc: jobregistry.GetAttrs}
	if err := store.CompleteWithOptions(options); err != nil {
		return nil, nil, err
	}

	statusStore := ptr.Clone(store)
	statusStore.UpdateStrategy = jobregistry.StatusStrategy
	statusStore.ResetFieldsStrategy = jobregistry.StatusStrategy
	return &REST{store}, &StatusREST{store: statusStore}, nil
}

// StatusREST implements the REST endpoint for changing Job status.
type StatusREST struct{ store *genericregistry.Store }

// New returns an empty Job.
func (r *StatusREST) New() runtime.Object { return &batch.Job{} }

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
