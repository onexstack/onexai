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

package controlplanestore

import (
	"context"

	"github.com/onexstack/onexai/internal/pkg/declcrud"
)

// getByKey serves a Get from the lister's index instead of materialising every
// row. A missing object is (zero, false, nil), not an error.
func getByKey[M any, CR any](
	get func(name string) (CR, error), fromCR func(CR) M,
) func(context.Context, string) (M, bool, error) {
	return func(_ context.Context, name string) (M, bool, error) {
		item, err := get(name)
		if err != nil {
			var zero M
			if isNotFound(err) {
				return zero, false, nil
			}
			return zero, false, err
		}
		return fromCR(item), true, nil
	}
}

// getByNamespacedKey is getByKey with the namespace in the index. A bare name
// is ambiguous once the same name may exist in two namespaces, so a
// cross-namespace reference has to address both halves.
func getByNamespacedKey[M any, CR any](
	get func(namespace, name string) (CR, error), fromCR func(CR) M,
) func(context.Context, string, string) (M, bool, error) {
	return func(_ context.Context, namespace, name string) (M, bool, error) {
		item, err := get(namespace, name)
		if err != nil {
			var zero M
			if isNotFound(err) {
				return zero, false, nil
			}
			return zero, false, err
		}
		return fromCR(item), true, nil
	}
}

// rowNamespaces is the namespace wiring for a resource whose rows carry a
// namespace: create and update use the row's namespace, delete uses the
// configured namespace, and every write resolves the object's real namespace
// from the cache.
func rowNamespaces[M any, CR objectMeta](
	namespace string, list func() ([]CR, error), rowNamespaceOf func(M) string,
) declcrud.Namespaces[M] {
	return declcrud.Namespaces[M]{
		Create:  rowNamespaceOf,
		Update:  rowNamespaceOf,
		Delete:  func() string { return writeNamespace(namespace) },
		Resolve: func(key, fallback string) (string, error) {
			return resolveNamespace(list, key, fallback)
		},
	}
}

// fixedNamespaces is rowNamespaces for a kind with no namespace column: every
// write lands in the configured namespace, else the default namespace.
func fixedNamespaces[M any, CR objectMeta](
	namespace string, list func() ([]CR, error),
) declcrud.Namespaces[M] {
	return rowNamespaces(namespace, list, func(M) string { return writeNamespace(namespace) })
}
