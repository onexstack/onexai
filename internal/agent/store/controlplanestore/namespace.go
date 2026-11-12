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
	"sort"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/pkg/declclient/declstore"
)

// objectMeta is the part of a control-plane object a write needs to locate it.
// Every object served by this package embeds metav1.ObjectMeta, so its
// accessors are enough to name the object and the namespace it lives in.
type objectMeta interface {
	GetName() string
	GetNamespace() string
}

// resolveNamespace reports the namespace the named object lives in, from the
// cluster-wide cache; fallback is used when the cache does not hold it yet.
//
// # Why a write has to resolve it
//
// The stores are constructed with the *configured* control-plane namespace,
// which a deployment that reads every namespace leaves empty. A namespaced
// resource has no cluster-wide object URL, so a Get/Update/Delete issued that
// way is answered with 404 "the server could not find the requested resource" —
// the write never reaches the object, and the error surfaces far away as a 500
// from the service that called the store.
//
// Create already picks the object's namespace (rowNamespace). An
// Update or Delete must land in the same namespace as the object it names, and
// for an object this process did not create the only authoritative answer is
// the cache.
//
// More than one match is refused: a delete that guessed would remove another
// tenant's object, and a list that guessed would address the wrong one.
func resolveNamespace[T objectMeta](list func() ([]T, error), name, fallback string) (string, error) {
	items, err := list()
	if err != nil {
		return fallback, nil
	}
	var namespaces []string
	for _, item := range items {
		if item.GetName() == name {
			namespaces = append(namespaces, item.GetNamespace())
		}
	}
	switch len(namespaces) {
	case 0:
		return fallback, nil
	case 1:
		return namespaces[0], nil
	default:
		sort.Strings(namespaces)
		return "", &declstore.AmbiguousKeyError{Key: name, Namespaces: dedupeSorted(namespaces)}
	}
}

// uniqueByName returns the one object with this name and whether it was found.
// More than one match is an ambiguity error: a Get that picks one would let a
// caller read another tenant's object.
func uniqueByName[T objectMeta](items []T, name string) (T, bool, error) {
	var zero T
	var matches []T
	for _, item := range items {
		if item.GetName() == name {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return zero, false, nil
	case 1:
		return matches[0], true, nil
	default:
		namespaces := make([]string, 0, len(matches))
		for _, item := range matches {
			namespaces = append(namespaces, item.GetNamespace())
		}
		sort.Strings(namespaces)
		return zero, false, &declstore.AmbiguousKeyError{Key: name, Namespaces: dedupeSorted(namespaces)}
	}
}

// dedupeSorted removes adjacent duplicates from a sorted namespace list.
func dedupeSorted(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

// writeNamespace is where an object with no namespace of its own is written:
// the configured namespace, else the default namespace — which is where the
// SQL->CRD migration placed the kinds that carry no namespace column (prompts,
// webhooks, models).
//
// The fallback used to be literally "default"; the default namespace is the
// namespace the platform's objects actually live in, so a deployment that left
// the namespace unset wrote to a namespace no reader ever looks at.
func writeNamespace(configured string) string {
	if configured != "" {
		return configured
	}
	return model.DefaultNamespace
}
