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

package validation

import (
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

const (
	// maxClusterBindingNamespaces caps the tenants one cluster binding may
	// address. A list longer than this is a modelling error, not a preference:
	// platform presets target a handful of business lines, not a thousand.
	maxClusterBindingNamespaces = 64
)

// ValidateClusterAgentBinding checks a cluster-wide binding before it is
// persisted.
//
// Every reference must carry a namespace. A cluster-scoped object has no
// namespace to default to, and guessing one would make the same spec mean
// different things depending on where it was written.
func ValidateClusterAgentBinding(spec *agent.ClusterAgentBindingSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if len(spec.Namespaces) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("namespaces"),
			"a cluster binding must name at least one namespace"))
	}
	if len(spec.Namespaces) > maxClusterBindingNamespaces {
		allErrs = append(allErrs, field.TooMany(fldPath.Child("namespaces"),
			len(spec.Namespaces), maxClusterBindingNamespaces))
	}
	wildcard := false
	for i, namespace := range spec.Namespaces {
		if namespace == agent.AllNamespaces {
			wildcard = true
			continue
		}
		if namespace == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("namespaces").Index(i),
				"a namespace must not be empty"))
			continue
		}
		for _, msg := range validation.IsDNS1123Label(namespace) {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("namespaces").Index(i), namespace, msg))
		}
	}
	if wildcard && len(spec.Namespaces) > 1 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("namespaces"), spec.Namespaces,
			`the wildcard "*" must be the only entry`))
	}

	if spec.AgentSelector == nil && len(spec.Agents) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("agentSelector"),
			"a binding must select at least one agent: set agentSelector, agents, or both"))
	}
	if spec.AgentSelector != nil {
		allErrs = append(allErrs, validateSelector(spec.AgentSelector, fldPath.Child("agentSelector"))...)
	}
	for i := range spec.Agents {
		refPath := fldPath.Child("agents").Index(i)
		allErrs = append(allErrs, validateReference(&spec.Agents[i], refPath)...)
		allErrs = append(allErrs, requireNamespace(spec.Agents[i].Namespace, refPath.Child("namespace"))...)
	}

	refLists := []struct {
		name string
		refs []agent.Reference
		kind string
	}{
		{"prompts", spec.Prompts, sharing.KindPrompt},
		{"skills", spec.Skills, sharing.KindSkill},
		{"knowledgeBases", spec.KnowledgeBases, sharing.KindKnowledgeBase},
		{"mcpservers", spec.MCPServers, sharing.KindMCPServer},
		{"customCommands", spec.CustomCommands, sharing.KindCustomCommand},
	}
	for _, list := range refLists {
		path := fldPath.Child(list.name)
		if len(list.refs) > maxBindingRefsPerKind {
			allErrs = append(allErrs, field.TooMany(path, len(list.refs), maxBindingRefsPerKind))
		}
		if len(list.refs) > 0 && !sharing.Shareable(list.kind) {
			// A cluster binding has no namespace of its own, so it cannot give
			// a same-namespace-only kind to every tenant: the controller would
			// mark each one missing. Refusing the write is the honest answer.
			allErrs = append(allErrs, field.Forbidden(path,
				"a cluster binding cannot contribute "+list.name+": this kind is not shareable across namespaces"))
		}
		for i := range list.refs {
			refPath := path.Index(i)
			allErrs = append(allErrs, validateReference(&list.refs[i], refPath)...)
			allErrs = append(allErrs, requireNamespace(list.refs[i].Namespace, refPath.Child("namespace"))...)
		}
	}
	if spec.MemoryRef != nil {
		refPath := fldPath.Child("memoryRef")
		allErrs = append(allErrs, validateReference(spec.MemoryRef, refPath)...)
		allErrs = append(allErrs, requireNamespace(spec.MemoryRef.Namespace, refPath.Child("namespace"))...)
	}
	if spec.ModelRef != nil {
		refPath := fldPath.Child("modelRef")
		allErrs = append(allErrs, validateReference(spec.ModelRef, refPath)...)
		allErrs = append(allErrs, requireNamespace(spec.ModelRef.Namespace, refPath.Child("namespace"))...)
	}

	if spec.CommandSelector != nil {
		allErrs = append(allErrs, validateCommandSelector(spec.CommandSelector,
			fldPath.Child("commandSelector"))...)
	}

	return allErrs
}

// requireNamespace refuses a reference without one. Shared references are
// explicit by construction; defaulting would invent an owner.
func requireNamespace(namespace string, fldPath *field.Path) field.ErrorList {
	if namespace != "" {
		return nil
	}
	return field.ErrorList{field.Required(fldPath,
		"a cluster binding reference must name the namespace it belongs to")}
}

// CanonicalizeClusterAgentBindingSpec deduplicates and sorts every list so that
// two spellings of the same binding produce the same stored object.
func CanonicalizeClusterAgentBindingSpec(spec *agent.ClusterAgentBindingSpec) {
	spec.Namespaces = sortedUnique(spec.Namespaces)
	spec.Agents = sortedUniqueReferences(spec.Agents)
	spec.Prompts = sortedUniqueReferences(spec.Prompts)
	spec.Skills = sortedUniqueReferences(spec.Skills)
	spec.KnowledgeBases = sortedUniqueReferences(spec.KnowledgeBases)
	spec.MCPServers = sortedUniqueReferences(spec.MCPServers)
	spec.CustomCommands = sortedUniqueReferences(spec.CustomCommands)
	spec.Commands = sortedUnique(spec.Commands)
}

// IsAllNamespaces reports whether the list is the wildcard spelling, which is
// a single AllNamespaces entry.
func IsAllNamespaces(namespaces []string) bool {
	return len(namespaces) == 1 && namespaces[0] == agent.AllNamespaces
}

// NamespaceInList reports whether a namespace is one of the binding's targets.
// The wildcard matches every namespace; it is the only value that does.
func NamespaceInList(namespaces []string, namespace string) bool {
	for _, candidate := range namespaces {
		if candidate == agent.AllNamespaces || candidate == namespace {
			return true
		}
	}
	return false
}
