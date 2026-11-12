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
	"github.com/onexstack/onexai/pkg/declclient/declstore"
)

// Reader and Store are the shared control-plane store primitives; this package
// only supplies the per-resource conversions and the write callbacks.
//
// Every resource in this package is its own file and its own named type, in the
// shape the store README describes: the five CRUD methods are inherited from
// Store and a resource overrides only what genuinely differs.
type Reader[M any] = declstore.Reader[M]

// Store is the shared five-method CRUD surface.
type Store[M any] = declstore.Store[M]

// ErrUnsupportedQuery and ErrManagedByControlPlane are re-exported so callers of
// this package do not need to import the primitives package.
var (
	ErrUnsupportedQuery      = declstore.ErrUnsupportedQuery
	ErrManagedByControlPlane = declstore.ErrManagedByControlPlane
)

// rowNamespace picks the namespace a row belongs to: its own namespace when set,
// the store's configured namespace otherwise, else the default namespace — the
// namespace the migration placed namespace-less objects in. Returning the
// literal "default" here sent every write of such a row to a namespace no
// reader ever looks at.
func rowNamespace(namespace, fallback string) string {
	if namespace != "" {
		return namespace
	}
	return writeNamespace(fallback)
}
