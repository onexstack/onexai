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

// Package declcrud builds a declstore.Store from a small descriptor.
//
// A control-plane store is the same wiring for every resource: a Reader over the
// informer cache, a create that picks the row's namespace, an update that
// finds the object where it lives, and a delete that treats "already gone" as
// success. Only the conversion pair and the namespace rules differ, so they are
// the descriptor and the rest lives here once.
package declcrud

import (
	"context"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/onexstack/onexai/pkg/declclient/declstore"
)

// Object is the part of a control-plane object a store needs: identity for
// addressing and a deep copy for the unchanged-update check.
type Object interface {
	metav1.Object
	runtime.Object
}

// API is the namespaced typed client surface a CRUD store needs.
type API[CR Object] interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (CR, error)
	Create(ctx context.Context, obj CR, opts metav1.CreateOptions) (CR, error)
	Update(ctx context.Context, obj CR, opts metav1.UpdateOptions) (CR, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
}

// Namespaces says where an object is written and where an existing one is
// addressed. A nil hook falls back to Spec.Namespace.
type Namespaces[M any] struct {
	// Create returns the namespace a create writes to.
	Create func(M) string
	// Update returns the fallback namespace for an update.
	Update func(M) string
	// Delete returns the fallback namespace for a delete.
	Delete func() string
	// Resolve, when set, finds the namespace an object actually lives in,
	// falling back to the value passed in. An error means the name matched more
	// than one namespace: the write is refused rather than aimed at a guess.
	Resolve func(key, fallback string) (string, error)
}

// Spec describes one row <-> CRD projection.
type Spec[M any, CR Object] struct {
	// Namespace is the store's configured namespace and the fallback for every
	// namespace hook.
	Namespace string
	API       func(namespace string) API[CR]
	// GetByKey, when set, serves Get from the lister's index instead of
	// materialising every row.
	GetByKey func(ctx context.Context, key string) (M, bool, error)
	// GetByNamespacedKey, when set, serves a Get that also filters on namespace.
	// It is what makes two same-named objects in different namespaces
	// addressable.
	GetByNamespacedKey func(ctx context.Context, namespace, key string) (M, bool, error)
	// List reads the cluster-wide lister.
	List   func() ([]CR, error)
	FromCR func(ctx context.Context, cr CR) (M, error)
	ToCR   func(ctx context.Context, row *M, cr CR) error
	// New builds the object a create sends.
	New   func(name, namespace string) CR
	Key   string
	KeyOf func(M) string
	// NameOf is the object name the row addresses.
	NameOf     func(M) string
	Column     declstore.Column[M]
	Namespaces Namespaces[M]
	// SkipUnchanged skips an update whose round-trip is identical. Only a store
	// that is re-asserted on a hot path needs it.
	SkipUnchanged bool
}

// NewStore builds the store from a descriptor.
func NewStore[M any, CR Object](spec Spec[M, CR]) *declstore.Store[M] {
	createNamespace := func(row *M) string {
		if spec.Namespaces.Create != nil {
			return spec.Namespaces.Create(*row)
		}
		return spec.Namespace
	}
	updateFallback := func(row *M) string {
		if spec.Namespaces.Update != nil {
			return spec.Namespaces.Update(*row)
		}
		return spec.Namespace
	}
	deleteFallback := func() string {
		if spec.Namespaces.Delete != nil {
			return spec.Namespaces.Delete()
		}
		return spec.Namespace
	}
	resolve := func(key, fallback string) (string, error) {
		if spec.Namespaces.Resolve != nil {
			return spec.Namespaces.Resolve(key, fallback)
		}
		return fallback, nil
	}

	return &declstore.Store[M]{
		Reader: declstore.Reader[M]{
			Key:                spec.Key,
			KeyOf:              spec.KeyOf,
			GetByKey:           spec.GetByKey,
			GetByNamespacedKey: spec.GetByNamespacedKey,
			All: func(ctx context.Context) ([]M, error) {
				items, err := spec.List()
				if err != nil {
					return nil, err
				}
				rows := make([]M, 0, len(items))
				for _, item := range items {
					row, err := spec.FromCR(ctx, item)
					if err != nil {
						return nil, err
					}
					rows = append(rows, row)
				}
				return rows, nil
			},
			Column: spec.Column,
		},
		CreateFunc: func(ctx context.Context, row *M) error {
			namespace := createNamespace(row)
			obj := spec.New(spec.NameOf(*row), namespace)
			if err := spec.ToCR(ctx, row, obj); err != nil {
				return err
			}
			_, err := spec.API(namespace).Create(ctx, obj, metav1.CreateOptions{})
			return err
		},
		UpdateFunc: func(ctx context.Context, row *M) error {
			key := spec.NameOf(*row)
			namespace, err := resolve(key, updateFallback(row))
			if err != nil {
				return err
			}
			api := spec.API(namespace)
			obj, err := api.Get(ctx, key, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if spec.SkipUnchanged {
				// Taken before ToCR touches obj: a real change must never look
				// like a no-op.
				before := obj.DeepCopyObject()
				if err := spec.ToCR(ctx, row, obj); err != nil {
					return err
				}
				if apiequality.Semantic.DeepEqual(before, obj) {
					return nil
				}
			} else if err := spec.ToCR(ctx, row, obj); err != nil {
				return err
			}
			_, err = api.Update(ctx, obj, metav1.UpdateOptions{})
			return err
		},
		DeleteFunc: func(ctx context.Context, name string) error {
			namespace, err := resolve(name, deleteFallback())
			if err != nil {
				return err
			}
			err = spec.API(namespace).Delete(ctx, name, metav1.DeleteOptions{})
			// Deleting something already gone is success; a retried delete must not
			// surface as an error.
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		},
	}
}
