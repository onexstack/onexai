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
	"k8s.io/apimachinery/pkg/runtime"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AuthorizationRule grants or denies a set of principals the right to use a
// kind of resource, optionally scoped to the agents it applies to and to
// attributes of the concrete resource instance.
//
// It is the declarative replacement for the agent.authz_rule table: the rule
// set is read by the agent runtime through a watch, so a change reaches every
// replica in one event instead of one poll interval.
type AuthorizationRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the desired rule.
	// +optional
	Spec AuthorizationRuleSpec `json:"spec,omitempty"`

	// Status is the observed state of the rule.
	// +optional
	Status AuthorizationRuleStatus `json:"status,omitempty"`
}

// AuthorizationRuleSpec defines the desired state of AuthorizationRule.
type AuthorizationRuleSpec struct {
	// Subjects lists the principals this rule speaks about. An empty list
	// matches nobody, which is the fail-closed default.
	// +optional
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=name
	Subjects []rbacv1.Subject `json:"subjects,omitempty"`

	// SubjectNamespace turns on the inherited match: a subject in this namespace
	// is matched through Namespace -> Group -> User, the way the Rego policy
	// has always resolved Kubernetes groups. Empty means no inheritance.
	// +optional
	SubjectNamespace string `json:"subjectNamespace,omitempty"`

	// AgentNames scopes the rule to the named agents. Empty means the rule is
	// not scoped by name.
	// +optional
	AgentNames []string `json:"agentNames,omitempty"`

	// AgentSelector scopes the rule to agents by label. Absent means no label
	// selection; an explicitly empty selector ({}) selects every agent in the
	// namespace, and is reported through the MatchesAllAgents condition.
	// +optional
	AgentSelector *metav1.LabelSelector `json:"agentSelector,omitempty"`

	// Resource is what the rule is about.
	//
	// It is the single-resource shape every rule written before the console's
	// policy editor used. New writes may instead use Resources; a reader must
	// evaluate both, because either is a complete statement on its own.
	// +optional
	Resource AuthorizationResource `json:"resource,omitempty"`

	// Resources is the multi-resource shape: one policy can name a namespace,
	// an agent and an MCP server without being split into three objects.
	//
	// At least one of Resource and Resources must be present. When both are,
	// their union is the rule's resource set — the single field stays populated
	// for readers that predate this one.
	// +optional
	// +listType=map
	// +listMapKey=kind
	Resources []AuthorizationResource `json:"resources,omitempty"`

	// Verbs are the operations granted, in the Kubernetes vocabulary:
	// get, list, watch, create, update, patch, delete, plus the custom verb
	// "use" ("may this principal run this tool or agent") and "*".
	//
	// The Rego policy still speaks read/write internally; the loader maps
	// get|list|watch to read and create|update|patch|delete to write, so the
	// stored policy is unchanged while the API is spelled the way a Kubernetes
	// user expects.
	Verbs []string `json:"verbs"`

	// Effect is Allow or Deny. A Deny on any matching rule wins.
	Effect AuthorizationEffect `json:"effect"`

	// Attributes restricts the rule to resources whose extracted attributes
	// match every key here. Values are strings (exact or glob) or lists of
	// strings. Empty means no attribute filter.
	// +optional
	Attributes *runtime.RawExtension `json:"attributes,omitempty"`

	// Enabled toggles the rule without deleting it.
	// +optional
	Enabled bool `json:"enabled"`

	// Description is free-form text for operators.
	// +optional
	Description string `json:"description,omitempty"`
}

// AuthorizationResource identifies what a rule addresses.
type AuthorizationResource struct {
	// Kind is the resource kind the Rego policy evaluates: agent, mcp, tool,
	// skill, knowledgebase, namespace, or "*" for any of them. The legacy
	// spelling "workspace" is still accepted.
	Kind string `json:"kind"`

	// Names lists the resource names. "*" means every name of that kind.
	// +optional
	Names []string `json:"names,omitempty"`
}

// AuthorizationEffect is the outcome a matching rule asks for.
type AuthorizationEffect string

const (
	// EffectAllow permits the request when no Deny matches.
	EffectAllow AuthorizationEffect = "Allow"
	// EffectDeny refuses the request.
	EffectDeny AuthorizationEffect = "Deny"
)

// AuthorizationRuleStatus defines the observed state of AuthorizationRule.
type AuthorizationRuleStatus struct {
	// ObservedGeneration is the generation the controller last acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// MatchedAgents is how many agents the name list and selector currently
	// resolve to. Zero with no selector means the rule is unscoped.
	// +optional
	MatchedAgents int32 `json:"matchedAgents,omitempty"`

	// Conditions describe the rule's health. Loaded is set once the controller
	// has seen and understood the rule; MatchesAllAgents warns that a selector
	// selects every agent in the namespace.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AuthorizationRuleList is a list of AuthorizationRule objects.
type AuthorizationRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is a list of schema objects.
	Items []AuthorizationRule `json:"items"`
}
