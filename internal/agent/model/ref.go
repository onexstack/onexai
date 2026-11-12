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

package model

import (
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

// Ref is a reference to a platform resource identified by (namespace, name).
//
// A namespace is part of the identity, not a hint: once two namespaces may hold
// the same name, a lookup keyed by name alone is ambiguous, and "which object did
// this run use" stops being answerable. Every reference that crosses a store
// boundary is therefore carried as a Ref.
type Ref struct {
	Namespace string
	Name      string
}

// Canonical renders the storage spelling, "namespace/name". An empty name
// yields the empty string, which is how "no reference" is stored.
func (r Ref) Canonical() string {
	if r.Name == "" {
		return ""
	}
	return NormalizeNamespace(r.Namespace) + "/" + r.Name
}

// SplitRef parses a canonical entry. A bare entry (no slash) has an empty
// namespace; the caller supplies its own default. The parser lives in sharing
// so admission, the controllers and the run path cannot disagree about it.
func SplitRef(entry string) Ref {
	namespace, name := sharing.SplitRef(entry)
	return Ref{Namespace: namespace, Name: name}
}

// ResolveRef parses entry against a default namespace: a bare name belongs to
// defaultNamespace, a qualified entry names its own.
func ResolveRef(defaultNamespace, entry string) Ref {
	namespace, name := sharing.SplitRef(entry)
	if namespace == "" {
		namespace = defaultNamespace
	}
	return Ref{Namespace: NormalizeNamespace(namespace), Name: name}
}

// CanonicalEntry resolves one entry against referrerNamespace and renders it
// as "namespace/name". It is the single normalization every run path should
// apply before a reference reaches a store lookup.
func CanonicalEntry(referrerNamespace, entry string) string {
	return sharing.CanonicalEntry(referrerNamespace, entry)
}

// CanonicalRef renders (namespace, name) as a canonical entry, defaulting an
// empty namespace.
func CanonicalRef(namespace, name string) string {
	return Ref{Namespace: namespace, Name: name}.Canonical()
}

// RefOf builds a Ref, treating an empty namespace as defaultNamespace.
func RefOf(defaultNamespace, namespace, name string) Ref {
	if namespace == "" {
		namespace = defaultNamespace
	}
	return Ref{Namespace: NormalizeNamespace(namespace), Name: name}
}

// Filter is the equality filter a store lookup for this reference uses. The
// namespace predicate is omitted when the reference does not carry one, which
// keeps a legacy bare entry resolving the way it always did.
func (r Ref) Filter() *where.Options {
	opts := where.F("name", r.Name)
	if r.Namespace != "" {
		opts = opts.F("namespace", r.Namespace)
	}
	return opts
}

// EntryFilter is Filter for a canonical "namespace/name" entry.
func EntryFilter(entry string) *where.Options {
	return SplitRef(entry).Filter()
}

// BareEntries renders canonical entries for the API edge: an entry in the
// object's own namespace is written as a bare name, and only a cross-namespace
// one keeps its qualifier.
//
// The REST contract is a string list, so this is what keeps the common case
// legible and the uncommon case explicit. It is also what lets an existing
// client keep writing bare names: ResolveRef gives them the same meaning.
func BareEntries(namespace string, entries []string) []string {
	if len(entries) == 0 {
		return entries
	}
	owner := NormalizeNamespace(namespace)
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		ref := ResolveRef(namespace, entry)
		if ref.Namespace == owner {
			out = append(out, ref.Name)
			continue
		}
		out = append(out, ref.Canonical())
	}
	return out
}

// BareEntry is BareEntries for one entry.
func BareEntry(namespace, entry string) string {
	if entry == "" {
		return ""
	}
	return BareEntries(namespace, []string{entry})[0]
}
