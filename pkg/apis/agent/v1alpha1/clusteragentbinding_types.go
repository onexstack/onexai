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

package v1alpha1

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AllNamespaces is the wildcard entry for ClusterAgentBindingSpec.Namespaces.
//
// It must be the only entry in the list. On its own it states "every
// namespace, including namespaces created after this object" — the deliberate
// opposite of the default explicit list, where a tenant added later is not in
// the audience until someone edits the binding. Mixed with names it would be a
// list whose meaning depends on which entry the reader noticed first, so the
// API refuses that spelling instead of picking one.
const AllNamespaces = "*"

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterAgentBinding applies one capability set to agents across several
// namespaces. It is the agent-level analogue of a ClusterRoleBinding: the
// binding lives at cluster scope because its audience does, while every
// capability it contributes still lives in its owner's namespace and still has
// to admit the agent that receives it.
//
// The union is purely additive, exactly like AgentBinding: no deny, no priority,
// no override. The two kinds differ only in reach — AgentBinding addresses
// agents inside one namespace, ClusterAgentBinding addresses an explicit list of
// namespaces.
type ClusterAgentBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the desired binding.
	// +optional
	Spec ClusterAgentBindingSpec `json:"spec,omitempty"`

	// Status is the observed binding.
	// +optional
	Status ClusterAgentBindingStatus `json:"status,omitempty"`
}

// ClusterAgentBindingSpec defines the desired state of ClusterAgentBinding.
type ClusterAgentBindingSpec struct {
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

	// Namespaces names the tenants whose agents this binding may contribute to.
	//
	// The list is explicit rather than a label selector on purpose: a grant must
	// be auditable by reading one object, and a namespace created later must not
	// silently inherit platform capabilities. Adding a tenant is a deliberate
	// edit, not an emergent effect of a label.
	//
	// The single entry AllNamespaces ("*") is the one way to say the opposite:
	// it states that the grant is meant for every namespace, including ones
	// created later. It is still an explicit edit an operator makes and still
	// readable from this one object — the wildcard is a statement about the
	// future, not an accident of a label — and it must be the only entry.
	// +listType=set
	Namespaces []string `json:"namespaces"`

	// AgentSelector selects agents inside each target namespace. An explicitly
	// empty selector ({}) selects every enabled agent there.
	// +optional
	AgentSelector *metav1.LabelSelector `json:"agentSelector,omitempty"`

	// Agents names agents outright, in addition to the selector. Each reference
	// must carry a namespace: a cluster-scoped object has no namespace to
	// default to.
	// +optional
	// +listType=atomic
	Agents []Reference `json:"agents,omitempty"`

	// Prompts names Prompt objects contributed to every selected agent.
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
	// agent. They must live in the target agent's own namespace.
	// +optional
	// +listType=atomic
	CustomCommands []Reference `json:"customCommands,omitempty"`

	// CommandSelector selects chat catalog commands by label, in addition to the
	// names in Commands. Union semantics: a command is granted when either the
	// list names it or the selector matches it.
	//
	// Every Command object carries its group as the label
	// agent.onexstack.io/group, so "group in (duty)" grants the whole duty group
	// — including a command added to that group later. The selector is kept as a
	// selector rather than expanded here so the grant follows the catalogue.
	//
	// An empty selector ({}) is refused on write: metav1.LabelSelectorAsSelector
	// turns it into Everything(), which would grant every command.
	// +optional
	CommandSelector *metav1.LabelSelector `json:"commandSelector,omitempty"`

	// Commands names chat catalog commands contributed to every selected agent.
	// The catalog is compiled into the chat server, so this list is not validated
	// against it here.
	// +optional
	Commands []string `json:"commands,omitempty"`

	// MemoryRef names a Memory contributed to every selected agent. It is a
	// scalar: an agent's own memoryRef always wins, and two bindings that name
	// different memories are a structural conflict reported through a condition
	// rather than resolved by an arbitrary pick.
	// +optional
	MemoryRef *Reference `json:"memoryRef,omitempty"`

	// ModelRef names a Model contributed to every selected agent. It follows
	// the same scalar precedence as MemoryRef: the agent's own modelRef wins,
	// and conflicting bindings are reported through a condition.
	// +optional
	ModelRef *Reference `json:"modelRef,omitempty"`
}

// ClusterAgentBindingStatus defines the observed state of ClusterAgentBinding.
type ClusterAgentBindingStatus struct {
	CommonStatus `json:",inline"`

	// MatchedNamespaces is how many of the declared namespaces exist.
	// +optional
	MatchedNamespaces int32 `json:"matchedNamespaces,omitempty"`

	// MatchedAgents is how many enabled agents the selectors resolve to across
	// every target namespace.
	// +optional
	MatchedAgents int32 `json:"matchedAgents,omitempty"`

	// Resolved is the deduplicated, sorted union of everything this binding
	// contributes. Entries are canonical "namespace/name".
	// +optional
	Resolved AgentBindingResolved `json:"resolved,omitempty"`

	// Missing lists references that do not exist, are disabled, or do not admit
	// the target namespaces, as "Kind/namespace/name".
	// +optional
	// +listType=atomic
	Missing []string `json:"missing,omitempty"`
}

// ClusterAgentBinding condition types beyond the shared ones.
const (
	// ConditionNamespaceMatched reports that at least one declared namespace
	// exists. A binding whose namespaces are all gone contributes nothing.
	ConditionNamespaceMatched = "NamespacesMatched"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterAgentBindingList is a list of ClusterAgentBinding objects.
type ClusterAgentBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ClusterAgentBinding `json:"items"`
}
