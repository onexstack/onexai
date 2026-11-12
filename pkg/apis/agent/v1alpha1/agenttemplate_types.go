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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentTemplate declares reusable agent behavior.
//
// A template is a named behavior unit, not an agent: it carries the prompt,
// model, capability references and run contract an agent would otherwise have
// to repeat, but no identity, no runtime and no authorization. An Agent opts
// into one or more templates through spec.templateRefs; the controller expands
// them into status.resolved, so editing a template changes every referencing
// agent without touching the agents themselves.
//
// The split is deliberate. Runtime placement belongs to RuntimeProfile and
// authorization belongs to the Agent, so a template stays portable across
// both: the same behavior can run on the in-process or an A2A runtime, and
// two teams can share it without sharing a security boundary.
type AgentTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the reusable behavior.
	// +optional
	Spec AgentTemplateSpec `json:"spec,omitempty"`

	// Status is the observed template. It carries the shared condition fields
	// only; templates have no controller of their own, because expansion
	// happens where a template is consumed.
	// +optional
	Status AgentTemplateStatus `json:"status,omitempty"`
}

// AgentTemplateSpec defines the behavior a template contributes.
//
// Every field has the same meaning it has on AgentSpec. The one semantic
// difference is precedence: when an agent both names a template and sets a
// field itself, the agent wins. Sets are unions; scalar fields fall back from
// the agent to the first template that declares them, in spec.templateRefs
// order.
type AgentTemplateSpec struct {
	CommonSpec `json:",inline"`

	// PromptRef names a single Prompt object. It is the compatibility spelling
	// of a one-element Prompts list.
	// +optional
	PromptRef *Reference `json:"promptRef,omitempty"`

	// Prompts names the Prompt objects bound to the template.
	// +optional
	// +listType=map
	// +listMapKey=name
	Prompts []Reference `json:"prompts,omitempty"`

	// PromptSelector selects Prompts by label.
	// +optional
	PromptSelector *metav1.LabelSelector `json:"promptSelector,omitempty"`

	// ModelRef names the default Model for agents that do not declare one.
	// +optional
	ModelRef *Reference `json:"modelRef,omitempty"`

	// Skills names Skill objects. Union with SkillSelector.
	// +optional
	// +listType=map
	// +listMapKey=name
	Skills []Reference `json:"skills,omitempty"`

	// SkillSelector selects Skills by label.
	// +optional
	SkillSelector *metav1.LabelSelector `json:"skillSelector,omitempty"`

	// MCPs names MCPServer objects. Union with MCPServerSelector.
	// +optional
	// +listType=map
	// +listMapKey=name
	MCPs []Reference `json:"mcps,omitempty"`

	// MCPServerSelector selects MCPServer objects by label.
	// +optional
	MCPServerSelector *metav1.LabelSelector `json:"mcpSelector,omitempty"`

	// KnowledgeBases names KnowledgeBase objects. Union with
	// KnowledgeBaseSelector.
	// +optional
	// +listType=map
	// +listMapKey=name
	KnowledgeBases []Reference `json:"knowledgeBases,omitempty"`

	// KnowledgeBaseSelector selects KnowledgeBase objects by label.
	// +optional
	KnowledgeBaseSelector *metav1.LabelSelector `json:"knowledgeBaseSelector,omitempty"`

	// CustomCommands names the user-defined slash commands this behavior
	// exposes. Union with CustomCommandSelector.
	// +optional
	// +listType=map
	// +listMapKey=name
	CustomCommands []Reference `json:"customCommands,omitempty"`

	// CustomCommandSelector selects CustomCommands by label.
	// +optional
	CustomCommandSelector *metav1.LabelSelector `json:"customCommandSelector,omitempty"`

	// Webhooks names Webhook objects called around runs of a referencing agent.
	// +optional
	Webhooks []string `json:"webhooks,omitempty"`

	// RequireApproval lists tool-name patterns that must be approved by a human.
	// +optional
	RequireApproval []string `json:"requireApproval,omitempty"`

	// Delegates names the Agents a referencing agent may call as tools.
	// +optional
	// +listType=map
	// +listMapKey=name
	Delegates []Reference `json:"delegates,omitempty"`

	// OutputSchema is the JSON Schema a successful terminal answer must match.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	OutputSchema *apiextensionsv1.JSON `json:"outputSchema,omitempty"`

	// OutputSchemaStrict asks providers that support strict schema generation
	// to constrain decoding.
	// +optional
	OutputSchemaStrict bool `json:"outputSchemaStrict,omitempty"`

	// SessionTools grants the run the session capability.
	// +optional
	SessionTools bool `json:"sessionTools,omitempty"`

	// MemoryRef names the Memory this template contributes to a referencing
	// agent. Empty contributes nothing.
	// +optional
	MemoryRef *Reference `json:"memoryRef,omitempty"`
}

// AgentTemplateStatus is the observed state of the template.
type AgentTemplateStatus struct {
	CommonStatus `json:",inline"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentTemplateList is a list of AgentTemplate objects.
type AgentTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []AgentTemplate `json:"items"`
}
