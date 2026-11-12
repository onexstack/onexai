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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Agent declares a persona and the capability set it may use.
//
// The capability set is a union: the names it lists plus everything the
// selectors match. Names are a pin — a selector that stops matching never
// removes a named skill — while selectors are how a new prod skill reaches the
// agents that opted into it without editing each one.
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the desired agent.
	// +optional
	Spec AgentSpec `json:"spec,omitempty"`

	// Status is the observed agent.
	// +optional
	Status AgentStatus `json:"status,omitempty"`
}

// AgentSpec defines the desired state of Agent.
type AgentSpec struct {
	CommonSpec `json:",inline"`

	// Kind says who owns this agent's behavior: native (the platform) or
	// external (a remote A2A AgentCard). Empty means native.
	// +optional
	// +kubebuilder:validation:Enum=native;external
	Kind AgentKind `json:"kind,omitempty"`

	// Execution selects the execution backend. Nil means inproc.
	// +optional
	Execution *AgentExecution `json:"execution,omitempty"`

	// Contract is the common, externally visible contract.
	// +optional
	Contract *AgentContract `json:"contract,omitempty"`

	// Sharing controls cross-namespace use of an external agent.
	// +optional
	Sharing *AgentSharing `json:"sharing,omitempty"`

	// Calls lists the agents this agent may call as tools. Any referenced
	// agent may be native or external; the runtime dispatches by target kind.
	// Only native agents may declare calls.
	// +optional
	// +listType=map
	// +listMapKey=name
	Calls []Reference `json:"calls,omitempty"`

	// PromptRef names a single Prompt object. It is the compatibility spelling
	// of a one-element Prompts list.
	// +optional
	PromptRef *Reference `json:"promptRef,omitempty"`

	// Prompts names the Prompt objects bound to this agent. The effective
	// prompt is derived from the union of PromptRef, Prompts and everything
	// PromptSelector matches.
	//
	// Atomic, not a name-keyed map: a reference is identified by
	// (namespace, name), so two namespaces may legitimately hold the same name.
	// Deduplication is done by the admission canonicalizer.
	// +optional
	// +listType=atomic
	Prompts []Reference `json:"prompts,omitempty"`

	// PromptSelector selects Prompts by label. Every match participates in the
	// union; an empty selector selects every prompt in the namespace.
	// +optional
	PromptSelector *metav1.LabelSelector `json:"promptSelector,omitempty"`

	// PromptAggregation controls the derived aggregated prompt. Nil means the
	// defaults.
	// +optional
	PromptAggregation *PromptAggregationSpec `json:"promptAggregation,omitempty"`

	// TemplateRefs names AgentTemplate objects whose behavior this agent
	// inherits. The list is ordered: sets are unioned, while a scalar field the
	// agent does not set falls back to the first template that declares it.
	//
	// Templates are same-namespace by construction, like aggregated agents:
	// the controller, the runtime store and the selector all address one
	// namespace.
	// +optional
	// +listType=map
	// +listMapKey=name
	TemplateRefs []Reference `json:"templateRefs,omitempty"`

	// Agents names Agent objects whose capability sets are aggregated into this
	// agent: their bound prompts, skills, MCP servers, knowledge bases, custom
	// commands, webhooks and approval patterns. It is the agent-level analogue
	// of an aggregated ClusterRole: a caller authorized for this agent gets the
	// union, without a binding on every source.
	//
	// The union is transitive and cycle-safe. A source must live in this agent's
	// namespace. Aggregation contributes capabilities only: a source's modelRef,
	// authzBackends and maxSkills are never inherited.
	// +optional
	// +listType=map
	// +listMapKey=name
	Agents []Reference `json:"agents,omitempty"`

	// AgentSelector selects Agents by label in this agent's namespace. Every
	// match participates in the union; an empty selector selects every agent in
	// the namespace except this one.
	// +optional
	AgentSelector *metav1.LabelSelector `json:"agentSelector,omitempty"`

	// Delegates names the Agents this agent may call as tools, through the
	// agent_invoke_agent capability. Delegation is not aggregation: the delegate
	// runs as its own run with its own prompt, sessions and runtime, and only
	// the request text and the answer cross the boundary.
	//
	// This list is the whole authorization for the capability. An agent with an
	// empty list is never granted the agent scope, so it cannot delegate even if
	// it asks. Delegates must live in this agent's namespace.
	// +optional
	// +listType=map
	// +listMapKey=name
	Delegates []Reference `json:"delegates,omitempty"`

	// ModelRef names the Model this agent runs on. Empty means the request's
	// model, then the deployment default.
	// +optional
	ModelRef *Reference `json:"modelRef,omitempty"`

	// Skills names Skill objects. Union with SkillSelector.
	//
	// Atomic, not a name-keyed map: a reference is identified by
	// (namespace, name), so two namespaces may legitimately hold the same name.
	// +optional
	// +listType=atomic
	Skills []Reference `json:"skills,omitempty"`

	// SkillSelector selects Skills by label. An absent selector selects nothing;
	// an explicitly empty selector ({}) selects every skill in the namespace and
	// is reported through the MatchesAllSkills condition.
	// +optional
	SkillSelector *metav1.LabelSelector `json:"skillSelector,omitempty"`

	// MCPs names MCPServer objects. Union with MCPServerSelector.
	//
	// Atomic, not a name-keyed map: a reference is identified by
	// (namespace, name), so two namespaces may legitimately hold the same name.
	// +optional
	// +listType=atomic
	MCPs []Reference `json:"mcps,omitempty"`

	// MCPServerSelector selects MCPServer objects by label.
	// +optional
	MCPServerSelector *metav1.LabelSelector `json:"mcpSelector,omitempty"`

	// KnowledgeBases names KnowledgeBase objects. Union with
	// KnowledgeBaseSelector.
	//
	// Atomic, not a name-keyed map: a reference is identified by
	// (namespace, name), so two namespaces may legitimately hold the same name.
	// +optional
	// +listType=atomic
	KnowledgeBases []Reference `json:"knowledgeBases,omitempty"`

	// KnowledgeBaseSelector selects KnowledgeBase objects by label.
	// +optional
	KnowledgeBaseSelector *metav1.LabelSelector `json:"knowledgeBaseSelector,omitempty"`

	// Webhooks names Webhook objects called around this agent's runs.
	// +optional
	Webhooks []string `json:"webhooks,omitempty"`

	// AuthnWebhooks names Webhook objects of type authentication that
	// authenticate callers of this agent's invocation entries. When non-empty,
	// a Bearer token is authenticated only by these TokenReview hooks and the
	// platform's JWT/OIDC chain is not consulted for this agent; AK/SK requests
	// are still verified by the platform. Empty uses platform authentication.
	// +optional
	AuthnWebhooks []string `json:"authnWebhooks,omitempty"`

	// AuthzWebhooks names Webhook objects of type authorization that decide
	// whether a caller may use this agent. When non-empty, only these
	// SubjectAccessReview hooks authorize the agent and the platform's
	// authorization (including authzBackends) is ignored for it. Empty uses
	// platform authorization.
	// +optional
	AuthzWebhooks []string `json:"authzWebhooks,omitempty"`

	// RequireApproval lists tool-name patterns that must be approved by a human:
	// an exact name, a "delete_*" prefix glob, or "*" for every tool.
	// +optional
	RequireApproval []string `json:"requireApproval,omitempty"`

	// AuthzBackends restricts the authorization backends evaluated for this
	// agent. Empty means every enabled backend; ["none"] opts the agent out of
	// Agent-level authorization.
	// +optional
	AuthzBackends []string `json:"authzBackends,omitempty"`

	// MaxSkills caps how many skills resolution may pull in. Zero means the
	// default (20). Exceeding it fails resolution instead of silently truncating.
	// +optional
	MaxSkills int32 `json:"maxSkills,omitempty"`

	// CustomCommands names the user-defined slash commands this agent exposes.
	// Union with CustomCommandSelector.
	// +optional
	// +listType=map
	// +listMapKey=name
	CustomCommands []Reference `json:"customCommands,omitempty"`

	// CustomCommandSelector selects CustomCommands by label.
	// +optional
	CustomCommandSelector *metav1.LabelSelector `json:"customCommandSelector,omitempty"`

	// Runtime selects the runtime contract. Nil means the deployment default
	// profile, which is the in-process eino runtime.
	// +optional
	Runtime *AgentRuntimeSpec `json:"runtime,omitempty"`

	// OutputSchema is the JSON Schema a successful terminal answer must match.
	// It is validated against the portable profile at admission and again at
	// the end of every run.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	OutputSchema *apiextensionsv1.JSON `json:"outputSchema,omitempty"`

	// OutputSchemaStrict asks providers that support strict schema generation
	// to constrain decoding. The gateway validates the answer regardless.
	// +optional
	OutputSchemaStrict bool `json:"outputSchemaStrict,omitempty"`

	// SessionTools grants the run the session capability: listing, reading,
	// continuing and forking the caller's own sessions over MCP.
	//
	// It is opt-in because those tools act on conversations other than the one
	// in progress. The tools only ever reach sessions the caller owns, but a
	// model that can enumerate a person's conversations is a different thing
	// from one that answers a question, so a deployment has to ask for it.
	// +optional
	SessionTools bool `json:"sessionTools,omitempty"`

	// MemoryRef names the Memory this agent uses. Nil disables long-term
	// memory; there is no implicit default.
	// +optional
	MemoryRef *Reference `json:"memoryRef,omitempty"`
}

// AgentMemorySpec is the controller-resolved effective memory configuration.
// It is status-only; users configure a Memory through AgentSpec.MemoryRef.
type AgentMemorySpec struct {
	// Namespace is the namespace the resolved Memory object lives in. It is not
	// necessarily the agent's: a shared Memory is used in place, so its entries
	// are written to the Memory's own namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Name is the resolved Memory object.
	// +optional
	Name string `json:"name,omitempty"`
	// Enabled turns memory on for this agent.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// EmbeddingModel names a Model used for embeddings.
	// +optional
	EmbeddingModel string `json:"embeddingModel,omitempty"`
	// TTLDays is how long a memory lives.
	// +optional
	TTLDays int32 `json:"ttlDays,omitempty"`
	// AutoSaveEvery is reserved for periodic summarization.
	// +optional
	AutoSaveEvery int32 `json:"autoSaveEvery,omitempty"`
}

// AgentRuntimeSpec selects a RuntimeProfile for an agent. The profile is the
// only runtime selector: its name is the runtime, so there is no second type
// enum to disagree with it.
type AgentRuntimeSpec struct {
	// ProfileRef names a RuntimeProfile in the agent's namespace. Empty selects
	// the deployment default profile.
	// +optional
	ProfileRef *Reference `json:"profileRef,omitempty"`
}

// AgentStatus defines the observed state of Agent.
type AgentStatus struct {
	CommonStatus `json:",inline"`

	// Resolved is the cache of what spec currently resolves to. It is derived,
	// never authoritative: edit spec, not this.
	// +optional
	Resolved AgentResolved `json:"resolved,omitempty"`

	// Missing lists the references the controller could not resolve, with the
	// reason in parentheses (not found, disabled, namespace not allowed).
	// +optional
	// +listType=atomic
	Missing []string `json:"missing,omitempty"`

	// ResolvedHash is a digest of Resolved, so a runtime can cheaply tell
	// whether its cached graph is still current.
	// +optional
	ResolvedHash string `json:"resolvedHash,omitempty"`

	// A2A is the observed card/endpoint/health of an A2A execution. It is
	// nil for native in-process agents.
	// +optional
	A2A *AgentA2AStatus `json:"a2a,omitempty"`

	// BindingContributions records which AgentBindings selected this agent and
	// what each contributed, so "why does this agent have this skill" is
	// answerable without re-running the match. Capped at 64 entries; the total is
	// reported separately.
	// +optional
	// +listType=atomic
	BindingContributions []AgentBindingContribution `json:"bindingContributions,omitempty"`

	// BindingContributionsTruncated reports that more than 64 bindings selected
	// this agent and the list above is incomplete.
	// +optional
	BindingContributionsTruncated bool `json:"bindingContributionsTruncated,omitempty"`

	// BindingContributionsTotal is the number of bindings that selected this
	// agent, including any omitted from the list.
	// +optional
	BindingContributionsTotal int32 `json:"bindingContributionsTotal,omitempty"`

	// AggregatedPrompt is the name of the derived Prompt object the runtime
	// uses. The controller produces it from the union of bound Prompts.
	// +optional
	AggregatedPrompt string `json:"aggregatedPrompt,omitempty"`

	// AggregatedInputHash digests the sources, model and budget that produced
	// the last controller-written aggregate. It mirrors the input-hash
	// annotation on the aggregated Prompt.
	// +optional
	AggregatedInputHash string `json:"aggregatedInputHash,omitempty"`

	// AggregatedPromptHash digests the last controller-written content. It
	// mirrors the content-hash annotation on the aggregated Prompt.
	// +optional
	AggregatedPromptHash string `json:"aggregatedPromptHash,omitempty"`

	// AggregatedPromptOverridden reports that the aggregated Prompt was edited
	// outside the controller. The controller preserves that content.
	// +optional
	AggregatedPromptOverridden bool `json:"aggregatedPromptOverridden,omitempty"`

	// AggregatedPromptStale reports that sources changed since the last
	// controller-written content or since an override was detected.
	// +optional
	AggregatedPromptStale bool `json:"aggregatedPromptStale,omitempty"`

	// ObservedRegenerateToken is the last regenerateToken the controller acted
	// on.
	// +optional
	ObservedRegenerateToken string `json:"observedRegenerateToken,omitempty"`
}

// PromptAggregationSpec controls the derived aggregated prompt.
type PromptAggregationSpec struct {
	// RegenerateToken forces one regeneration when it changes to a new
	// non-empty value. The controller records the value it acted on in
	// status.observedRegenerateToken; clearing the token does not regenerate.
	// +optional
	RegenerateToken string `json:"regenerateToken,omitempty"`
}

// AgentResolved is what an agent's spec resolves to at the observed generation.
type AgentResolved struct {
	// Prompt is the name of the effective aggregated Prompt.
	// +optional
	Prompt string `json:"prompt,omitempty"`
	// Prompts are the source Prompt names, in aggregation order.
	// +optional
	// +listType=atomic
	Prompts []string `json:"prompts,omitempty"`
	// Agents are the source Agent names, transitive and in aggregation order.
	// +optional
	// +listType=atomic
	Agents []string `json:"agents,omitempty"`
	// Templates are the source AgentTemplate names, in spec.templateRefs order.
	// +optional
	// +listType=atomic
	Templates []string `json:"templates,omitempty"`
	// +optional
	Model string `json:"model,omitempty"`
	// ModelNamespace is where the resolved Model lives. It is a sibling of
	// Model rather than a "namespace/name" string so existing readers of
	// Model keep seeing the bare name, while the runtime can address the row
	// unambiguously when two namespaces hold the same one.
	// +optional
	ModelNamespace string `json:"modelNamespace,omitempty"`
	// +optional
	Skills []string `json:"skills,omitempty"`
	// +optional
	MCPs []string `json:"mcps,omitempty"`
	// +optional
	KnowledgeBases []string `json:"knowledgeBases,omitempty"`
	// +optional
	Webhooks []string `json:"webhooks,omitempty"`
	// RequireApproval is the union of the approval patterns this agent and its
	// aggregated agents declare.
	// +optional
	RequireApproval []string `json:"requireApproval,omitempty"`
	// +optional
	CustomCommands []string `json:"customCommands,omitempty"`
	// Commands are the chat catalog commands contributed by bindings, sorted.
	// +optional
	Commands []string `json:"commands,omitempty"`
	// Delegates is the union of the agent's own delegates and the ones its
	// templates declare.
	// +optional
	// +listType=atomic
	Delegates []string `json:"delegates,omitempty"`
	// Calls is the resolved union of behavior.calls, template calls and
	// binding calls. A target may be native or external.
	// +optional
	// +listType=atomic
	Calls []string `json:"calls,omitempty"`
	// SessionTools is true when the agent or one of its templates opts in.
	// +optional
	SessionTools bool `json:"sessionTools,omitempty"`
	// OutputSchema is the effective schema: the agent's own when set, otherwise
	// the first template's.
	// +optional
	OutputSchema *apiextensionsv1.JSON `json:"outputSchema,omitempty"`
	// OutputSchemaStrict is the effective strict flag.
	// +optional
	OutputSchemaStrict bool `json:"outputSchemaStrict,omitempty"`
	// Memory is the effective memory configuration.
	// +optional
	Memory *AgentMemorySpec `json:"memory,omitempty"`
}

// AgentBindingContribution records one binding's effect on an agent.
type AgentBindingContribution struct {
	// Kind is AgentBinding or ClusterAgentBinding.
	// +optional
	Kind string `json:"kind,omitempty"`
	// Namespace is the binding's namespace. Empty for a cluster-wide binding.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Name is the binding name.
	Name string `json:"name"`
	// Generation is the binding generation that was merged.
	// +optional
	Generation int64 `json:"generation,omitempty"`
	// Hash digests the binding's resolved contribution, so a change is visible
	// without comparing the counts.
	// +optional
	Hash string `json:"hash,omitempty"`
	// +optional
	PromptCount int32 `json:"promptCount,omitempty"`
	// +optional
	SkillCount int32 `json:"skillCount,omitempty"`
	// +optional
	MCPCount int32 `json:"mcpCount,omitempty"`
	// +optional
	KnowledgeBaseCount int32 `json:"knowledgeBaseCount,omitempty"`
	// +optional
	CustomCommandCount int32 `json:"customCommandCount,omitempty"`
	// +optional
	CommandCount int32 `json:"commandCount,omitempty"`
}

// Agent condition types beyond the shared ones.
const (
	// ConditionSkillsWithinBudget reports that resolution stayed under MaxSkills.
	ConditionSkillsWithinBudget = "SkillsWithinBudget"
	// ConditionPromptAmbiguous is no longer set: every selector match now
	// participates in aggregation.
	//
	// Deprecated: retained for one release so existing readers do not break.
	ConditionPromptAmbiguous = "PromptAmbiguous"
	// ConditionPromptAggregated reports the state of the derived aggregated
	// prompt: generated, copied, overridden, stale, or failed.
	ConditionPromptAggregated = "PromptAggregated"
	// ConditionAgentAggregated reports the state of the spec.agents union:
	// aggregated, or a structural failure that made the controller drop the
	// inherited set (a cycle, a depth or source-count cap, or an unavailable
	// source).
	ConditionAgentAggregated = "AgentAggregated"
	// ConditionBindingsApplied reports the state of the AgentBinding union:
	// merged, no bindings matched, a contributing binding was stale, a binding
	// was skipped because the agent exceeded the binding cap, or resolution
	// failed.
	ConditionBindingsApplied = "BindingsApplied"
	// ConditionTemplatesApplied reports the state of the spec.templateRefs
	// union: every named template was applied, or one is missing or disabled.
	ConditionTemplatesApplied = "TemplatesApplied"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentList is a list of Agent objects.
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is a list of schema objects.
	Items []Agent `json:"items"`
}
