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

// Package agentlabel turns an agent record into the two selection spaces a
// command rule can address it in.
//
// # Two spaces, because Kubernetes has two selectors
//
// A Kubernetes object is addressable two ways that cannot see each other:
// `kubectl get pods -l app=foo` reads metadata.labels, and
// `kubectl get pods --field-selector metadata.name=foo` reads the object's
// fields. A label selector can never name metadata.name; a field selector can
// never name a label. That is not a workaround — it is why a free-form label
// can never collide with a property the system owns.
//
// This package applies the same split to an agent:
//
//   - Labels are the agent object's own metadata.labels. They are the record's
//     tags: the store writes each tag into the label set, so the API server
//     validates them and an operator can select on them with kubectl.
//   - Fields come from the record's own columns. Those are the properties the
//     system knows structurally, and only a field selector can reach them.
//
// Only the field half is built here. A label set needs no projection — it is
// already a legal label set, because it could not have been written otherwise.
package agentlabel

import (
	"k8s.io/apimachinery/pkg/fields"

	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

// Prefix is the domain of the label keys this service writes.
//
// A Kubernetes label key is [prefix/]name, and the prefix is the DNS subdomain
// of whoever wrote the label. Kubernetes keeps kubernetes.io/ and k8s.io/ for
// itself and leaves every other domain to its owner, which is what keeps two
// writers from colliding without either of them reserving a word: chat writes
// agent.onexstack.io/, the onex provisioner writes onex.example.com/, and
// neither has to know what the other calls things.
//
// It is the prefix of the labels the *command catalog* declares — a command's
// group and a skill's provider. Tags on an agent are not this service's to
// write and are matched exactly as the record holds them.
const Prefix = "agent.onexstack.io/"

// Field names an agent can be selected by.
//
// They are a closed set, and that is the point of the field space: Kubernetes
// rejects a field selector naming a field its resource does not have, which is
// how `metadata.namespce` is caught at write time. A label key has no such
// list — any legal key is a possible tag — so a mistyped label is a selector
// that silently matches nothing. The two spaces are not redundant; this is the
// property only one of them has.
const (
	// FieldName is the agent's name.
	FieldName = "metadata.name"
	// FieldNamespace is the agent's namespace, defaulted when the record leaves
	// it empty.
	FieldNamespace = "metadata.namespace"
	// LegacyFieldWorkspace is the pre-rename spelling of FieldNamespace. It is
	// published alongside it for one release so a selector written before the
	// rename keeps matching; nothing new should use it.
	LegacyFieldWorkspace = "metadata.workspace"
)

// FieldNames returns the field names an agent selector may use.
func FieldNames() []string { return []string{FieldName, FieldNamespace, LegacyFieldWorkspace} }

// DefaultNamespace is the namespace an agent with none set belongs to.
//
// It is the same constant the API and the run path use; keeping one value is
// what stops a rule written for the default from selecting an agent "in a
// namespace it does not have".
const DefaultNamespace = sharing.DefaultNamespace

// Fields turns an agent record's columns into its field set.
//
// The namespace is defaulted here rather than at the call site so that a rule
// written against the default selects the agents that never stated one — the
// alternative is an agent that is in helper-public for every purpose except
// being selected as such.
func Fields(name, namespace string) fields.Set {
	value := defaultNamespace(namespace)
	return fields.Set{
		FieldName:            name,
		FieldNamespace:       value,
		LegacyFieldWorkspace: value,
	}
}

// defaultNamespace substitutes the namespace an agent with none set belongs to.
func defaultNamespace(namespace string) string {
	if namespace == "" {
		return DefaultNamespace
	}
	return namespace
}
