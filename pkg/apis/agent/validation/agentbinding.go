// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
)

const (
	// maxBindingAgents caps the explicitly named agents. Selector matches are
	// bounded by the namespace instead, which is why they are not counted here.
	maxBindingAgents = 64
	// maxBindingRefsPerKind caps each reference list. One binding is meant to
	// carry a platform-level preset, not to become a bulk-edit tool.
	maxBindingRefsPerKind = 64
	// maxBindingCommands caps the named commands.
	maxBindingCommands = 128
)

// ValidateAgentBinding checks a binding before it is persisted.
//
// A binding is the one place an Agent may reach another namespace: a shareable
// kind may carry a namespace, and the controller checks the target namespace's
// ReferenceGrant before it contributes. A kind the sharing table marks
// unshareable stays same-namespace, because nothing could authorize it.
func ValidateAgentBinding(spec *agent.AgentBindingSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	// A binding must select something. A selector or a name list satisfies that;
	// requiring a selector even when every agent is named would force every
	// migration and every hand-written audience binding to carry a placeholder
	// selector that means nothing.
	if spec.Selector == nil && len(spec.Agents) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("selector"),
			"a binding must select at least one agent: set selector, agents, or both"))
	}
	if spec.Selector != nil {
		allErrs = append(allErrs, validateSelector(spec.Selector, fldPath.Child("selector"))...)
	}

	if len(spec.Agents) > maxBindingAgents {
		allErrs = append(allErrs, field.TooMany(fldPath.Child("agents"), len(spec.Agents), maxBindingAgents))
	}
	for i, name := range spec.Agents {
		refPath := fldPath.Child("agents").Index(i)
		if name == "" {
			allErrs = append(allErrs, field.Required(refPath, "an agent name must not be empty"))
			continue
		}
		for _, msg := range validation.IsDNS1123Subdomain(name) {
			allErrs = append(allErrs, field.Invalid(refPath, name, msg))
		}
	}

	refLists := []struct {
		name string
		refs []agent.Reference
		// kind is the sharing table's name for this list. A kind it marks
		// unshareable (a custom command) may not carry a namespace.
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
		for i := range list.refs {
			refPath := path.Index(i)
			allErrs = append(allErrs, validateReference(&list.refs[i], refPath)...)
			if list.refs[i].Namespace != "" && !sharing.Shareable(list.kind) {
				allErrs = append(allErrs, field.Invalid(refPath.Child("namespace"), list.refs[i].Namespace,
					"a "+list.name+" reference must be in the binding's namespace"))
			}
		}
	}

	if spec.ModelRef != nil {
		refPath := fldPath.Child("modelRef")
		allErrs = append(allErrs, validateReference(spec.ModelRef, refPath)...)
		// A model is shareable, so a namespace is admitted here; the controller
		// checks the target namespace's ReferenceGrant before it contributes.
	}

	if len(spec.Commands) > maxBindingCommands {
		allErrs = append(allErrs, field.TooMany(fldPath.Child("commands"), len(spec.Commands), maxBindingCommands))
	}
	for i, command := range spec.Commands {
		if command == "" {
			allErrs = append(allErrs, field.Required(fldPath.Child("commands").Index(i),
				"a command name must not be empty"))
		}
	}

	if spec.CommandSelector != nil {
		allErrs = append(allErrs, validateCommandSelector(spec.CommandSelector,
			fldPath.Child("commandSelector"))...)
	}

	return allErrs
}

// validateCommandSelector checks a binding's command selector.
//
// An explicitly empty selector is refused even though it parses: metav1 treats
// {} as Everything(), so accepting it would silently grant every command in the
// catalogue. The agent selector deliberately reads {} as "every agent"; there is
// no legitimate "every command", so the same spelling here can only be a
// half-filled form or a mistake.
func validateCommandSelector(selector *metav1.LabelSelector, fldPath *field.Path) field.ErrorList {
	if len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 {
		return field.ErrorList{field.Invalid(fldPath, selector,
			"a command selector must not be empty; an empty selector matches every command")}
	}
	return validateSelector(selector, fldPath)
}

// CanonicalizeAgentBindingSpec deduplicates and sorts every list so that two
// spellings of the same binding produce the same stored object. It is applied
// on write, which is what lets the union stay order-independent.
func CanonicalizeAgentBindingSpec(spec *agent.AgentBindingSpec) {
	spec.Agents = sortedUnique(spec.Agents)
	spec.Prompts = sortedUniqueReferences(spec.Prompts)
	spec.Skills = sortedUniqueReferences(spec.Skills)
	spec.KnowledgeBases = sortedUniqueReferences(spec.KnowledgeBases)
	spec.MCPServers = sortedUniqueReferences(spec.MCPServers)
	spec.CustomCommands = sortedUniqueReferences(spec.CustomCommands)
	spec.Commands = sortedUnique(spec.Commands)
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedUniqueReferences(refs []agent.Reference) []agent.Reference {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(refs))
	out := make([]agent.Reference, 0, len(refs))
	for _, ref := range refs {
		key := ref.Namespace + "/" + ref.Name
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// SelectorMatchesAll reports whether the selector is an explicit empty object.
func SelectorMatchesAll(selector *metav1.LabelSelector) bool {
	if selector == nil {
		return false
	}
	asSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return false
	}
	return asSelector.Empty()
}
