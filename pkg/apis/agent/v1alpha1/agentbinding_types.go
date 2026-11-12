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

package v1alpha1

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentBinding applies a capability set to every agent its selector matches.
//
// It is the agent-level analogue of a ClusterRoleBinding: the binding is the
// thing you create once, the selector is how it reaches a population, and the
// effective capability set of an agent is the union of its own spec and every
// binding that selects it. The union is purely additive — a binding can add a
// skill, never remove one, and two bindings that name the same object agree by
// construction. That is the same conflict model RBAC and NetworkPolicy use, and
// it is why there is no priority or deny field here.
type AgentBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the desired binding.
	// +optional
	Spec AgentBindingSpec `json:"spec,omitempty"`

	// Status is the observed binding.
	// +optional
	Status AgentBindingStatus `json:"status,omitempty"`
}

// AgentBindingSpec defines the desired state of AgentBinding.
//
// It does not embed CommonSpec: a binding is enabled unless it says otherwise,
// which needs a tri-state field. A plain bool cannot tell "absent" from
// "explicitly false", so embedding the shared struct would make every new
// binding silently inert.
type AgentBindingSpec struct {
	// Enabled toggles the binding without deleting it. Nil defaults to true.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// DisplayName is the human-facing name.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Description explains what the binding is for.
	// +optional
	Description string `json:"description,omitempty"`

	// Owner is the principal this binding belongs to. The immutable record of
	// who created it is the onexai.io/created-by annotation.
	// +optional
	Owner *rbacv1.Subject `json:"owner,omitempty"`

	// Selector selects the agents this binding applies to. Every match
	// participates; an explicitly empty selector ({}) selects every agent in the
	// namespace and is reported through the SelectorMatchesAllAgents condition.
	// One of Selector and Agents must be set: a binding that selects nothing is
	// indistinguishable from a typo.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Agents names agents outright, in addition to the selector. They use the
	// same union semantics: naming an agent is a pin that a later label change
	// does not remove.
	// +optional
	Agents []string `json:"agents,omitempty"`

	// ModelRef names the model endpoint this binding contributes. The agent's
	// own modelRef wins; a binding supplies one only when the agent declares
	// none. Two bindings naming different models are a structural conflict.
	// +optional
	ModelRef *Reference `json:"modelRef,omitempty"`

	// Prompts names Prompt objects contributed to every selected agent.
	//
	// Atomic: a reference is identified by (namespace, name), so two
	// namespaces may legitimately hold the same name.
	// +optional
	// +listType=atomic
	Prompts []Reference `json:"prompts,omitempty"`

	// Skills names Skill objects contributed to every selected agent.
	// +optional
	// +listType=atomic
	Skills []Reference `json:"skills,omitempty"`

	// KnowledgeBases names KnowledgeBase objects contributed to every selected
	// agent.
	// +optional
	// +listType=atomic
	KnowledgeBases []Reference `json:"knowledgeBases,omitempty"`

	// MCPServers names MCPServer objects contributed to every selected agent.
	// +optional
	// +listType=atomic
	MCPServers []Reference `json:"mcpservers,omitempty"`

	// CustomCommands names CustomCommand objects contributed to every selected
	// agent.
	// +optional
	// +listType=map
	// +listMapKey=name
	CustomCommands []Reference `json:"customCommands,omitempty"`

	// CommandSelector selects chat catalog commands by label, in addition to the
	// names in Commands. Union semantics: a command is granted when either the
	// list names it or the selector matches it.
	//
	// It is the batch axis the command catalogue was built for: every Command
	// object carries its group as the label agent.onexstack.io/group, so
	// "group in (duty)" grants the whole duty group — including a command added
	// to that group later. The selector is kept as a selector, not expanded
	// into names here, so the grant follows the catalogue.
	//
	// An empty selector ({}) is refused on write: metav1.LabelSelectorAsSelector
	// turns it into Everything(), which would grant every command in the
	// catalogue — the opposite of "I forgot to fill this in".
	// +optional
	CommandSelector *metav1.LabelSelector `json:"commandSelector,omitempty"`

	// Commands names chat catalog commands contributed to every selected agent,
	// e.g. "bind" or "skill:<name>". The catalog is compiled into the chat
	// server, so this list is not validated against it here.
	// +optional
	Commands []string `json:"commands,omitempty"`
}

// AgentBindingStatus defines the observed state of AgentBinding.
type AgentBindingStatus struct {
	CommonStatus `json:",inline"`

	// MatchedAgents is how many enabled agents the selector and name list
	// resolve to. The names themselves are not stored: the count is bounded by
	// the namespace, a list is not.
	// +optional
	MatchedAgents int32 `json:"matchedAgents,omitempty"`

	// Resolved is the deduplicated, sorted union of everything this binding
	// contributes. Agents read it directly instead of re-resolving references,
	// so the work happens once per binding rather than once per agent.
	// +optional
	Resolved AgentBindingResolved `json:"resolved,omitempty"`

	// Missing lists references that do not exist or are disabled, as
	// "Kind/namespace/name".
	// +optional
	// +listType=atomic
	Missing []string `json:"missing,omitempty"`
}

// AgentBindingResolved is what one binding contributes.
type AgentBindingResolved struct {
	// +optional
	Prompts []string `json:"prompts,omitempty"`
	// +optional
	Skills []string `json:"skills,omitempty"`
	// +optional
	MCPs []string `json:"mcps,omitempty"`
	// +optional
	KnowledgeBases []string `json:"knowledgeBases,omitempty"`
	// +optional
	CustomCommands []string `json:"customCommands,omitempty"`
	// +optional
	Commands []string `json:"commands,omitempty"`
}

// AgentBinding condition types beyond the shared ones.
const (
	// ConditionAgentsMatched reports that the selector resolved to a non-empty
	// agent set. A binding that matches nobody is not an error, but it is almost
	// always a label typo, so it is surfaced.
	ConditionAgentsMatched = "AgentsMatched"
	// ConditionSelectorMatchesAllAgents reports that the selector was written as
	// an explicit empty object, which selects every agent in the namespace.
	ConditionSelectorMatchesAllAgents = "SelectorMatchesAllAgents"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentBindingList is a list of AgentBinding objects.
type AgentBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []AgentBinding `json:"items"`
}
