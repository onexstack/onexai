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
	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&AgentM{}) }

// AgentM describes an AI agent: its persona (system prompt) and the sets of
// skills, MCP servers and knowledge bases it may use.
type AgentM struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string `gorm:"uniqueIndex;size:128" json:"name"`
	DisplayName string `gorm:"size:256" json:"display_name"`
	Status      string `gorm:"size:32" json:"status"`
	Description string `gorm:"type:text" json:"description"`
	Owner       string `gorm:"size:256" json:"owner"`
	// Model optionally pins this agent to a configured model endpoint
	// (agent.model.name). Empty means the run resolves the request's model,
	// then this field, then the deployment default — see the runtime's model
	// resolution.
	Model string `gorm:"size:128;index" json:"model"`
	// PromptRefs is the ordered list of Prompt names this agent may use as its
	// persona, derived from the Agent spec (promptRef, then prompts, same
	// namespace only). It is read from the control plane, never stored in the
	// table. The runtime tries them in order while the aggregated Prompt is not
	// available; empty means the engine's default instruction.
	PromptRefs StringList `gorm:"-" json:"prompts,omitempty"`
	// TemplateRefs is the ordered list of AgentTemplate names this agent
	// references, derived from the Agent spec (same namespace only). It is read
	// from the control plane, never stored in the table: the controller expands
	// the templates into status.resolved, and the runtime consumes that.
	TemplateRefs StringList `gorm:"-" json:"template_refs,omitempty"`
	// AggregatedPrompt is the name of the derived Prompt the runtime prefers.
	// It is read from the Agent status, not stored in the table.
	AggregatedPrompt string     `gorm:"-" json:"aggregated_prompt,omitempty"`
	Skills           StringList `gorm:"type:text" json:"skills"`
	MCPs             StringList `gorm:"column:mcps;type:text" json:"mcps"`
	KnowledgeBases   StringList `gorm:"type:text" json:"knowledge_bases"`
	RequireApproval  StringList `gorm:"type:text" json:"require_approval"`
	// CustomCommands names the user-defined slash commands this agent exposes.
	// ResolvedCustomCommands additionally carries selector matches, which only
	// the controller can compute; it is read from status, not stored in the table.
	CustomCommands         StringList `gorm:"-" json:"custom_commands,omitempty"`
	ResolvedCustomCommands StringList `gorm:"-" json:"resolved_custom_commands,omitempty"`
	// Delegates names the agents this agent may call as tools. It is read from
	// the spec, never stored in the table: the control plane is the source of
	// truth for an agent's declared authority, exactly like CustomCommands.
	Delegates StringList `gorm:"-" json:"delegates,omitempty"`

	// Kind is native or external. ExecutionJSON carries spec.execution as
	// JSON; ContractJSON/SharingJSON carry the common contract and sharing
	// policy. A2AStatusJSON carries status.a2a. They are projections of the
	// control-plane object and are never stored in the legacy agent table.
	Kind          string     `gorm:"-" json:"kind,omitempty"`
	Calls         StringList `gorm:"-" json:"calls,omitempty"`
	ExecutionJSON string     `gorm:"-" json:"execution,omitempty"`
	ContractJSON  string     `gorm:"-" json:"contract,omitempty"`
	SharingJSON   string     `gorm:"-" json:"sharing,omitempty"`
	A2AStatusJSON string     `gorm:"-" json:"a2a_status,omitempty"`
	// ResolvedMCPs carries selector-resolved MCP names, used to check that a
	// tool command's server is really bound to the agent. Like the two above it
	// is read from status.
	ResolvedMCPs StringList `gorm:"-" json:"resolved_mcps,omitempty"`
	// ResolvedSkills, ResolvedKnowledgeBases, ResolvedWebhooks and
	// ResolvedRequireApproval carry the controller-resolved unions — spec refs,
	// selector matches and everything inherited from aggregated agents. They
	// are read from status, never stored in the table, and the runtime prefers
	// them over the declared fields above.
	// Missing lists the references the controller could not resolve, with the
	// reason in parentheses.
	Missing                 StringList `gorm:"-" json:"missing,omitempty"`
	ResolvedSkills          StringList `gorm:"-" json:"resolved_skills,omitempty"`
	ResolvedKnowledgeBases  StringList `gorm:"-" json:"resolved_knowledge_bases,omitempty"`
	ResolvedWebhooks        StringList `gorm:"-" json:"resolved_webhooks,omitempty"`
	ResolvedRequireApproval StringList `gorm:"-" json:"resolved_require_approval,omitempty"`
	// ResolvedCommands carries the chat commands contributed by AgentBindings.
	// Like the sets above it is read from status and preferred by the runtime.
	ResolvedCommands StringList `gorm:"-" json:"resolved_commands,omitempty"`
	// ResolvedDelegates, ResolvedSessionTools and the resolved scalar fields
	// below carry what the controller made of spec.templateRefs: the union of
	// the agent's own declaration and the behavior its templates contribute.
	// They are read from status and preferred by the runtime, exactly like the
	// capability sets above; the declared fields stay the spec view the admin
	// API writes back.
	ResolvedTemplates          StringList `gorm:"-" json:"resolved_templates,omitempty"`
	ResolvedDelegates          StringList `gorm:"-" json:"resolved_delegates,omitempty"`
	ResolvedCalls              StringList `gorm:"-" json:"resolved_calls,omitempty"`
	ResolvedSessionTools       bool       `gorm:"-" json:"resolved_session_tools,omitempty"`
	ResolvedModel              string     `gorm:"-" json:"resolved_model,omitempty"`
	ResolvedOutputSchema       string     `gorm:"-" json:"resolved_output_schema,omitempty"`
	ResolvedOutputSchemaStrict bool       `gorm:"-" json:"resolved_output_schema_strict,omitempty"`
	ResolvedMemoryEnabled      bool       `gorm:"-" json:"resolved_memory_enabled,omitempty"`
	ResolvedMemoryName         string     `gorm:"-" json:"resolved_memory_name,omitempty"`
	ResolvedMemoryEmbedding    string     `gorm:"-" json:"resolved_memory_embedding_model,omitempty"`
	ResolvedMemoryTTLDays      int32      `gorm:"-" json:"resolved_memory_ttl_days,omitempty"`
	ResolvedMemoryAutoSave     int32      `gorm:"-" json:"resolved_memory_auto_save_every,omitempty"`
	// Observed reports that the controller has written status at least once.
	// Without it an empty resolved set is ambiguous: it could mean "the
	// controller resolved this agent to nothing" or "the controller has not run".
	// The first must not fall back to the declared set, or a binding removed from
	// an agent silently comes back.
	Observed bool `gorm:"-" json:"-"`
	// ResolvedHash is the controller's digest of the resolved set; it feeds the
	// runner cache fingerprint.
	ResolvedHash string `gorm:"-" json:"-"`
	// BindingContributions explains which AgentBindings selected this agent.
	BindingContributions          []BindingContributionM `gorm:"-" json:"binding_contributions,omitempty"`
	BindingContributionsTotal     int32                  `gorm:"-" json:"binding_contributions_total,omitempty"`
	BindingContributionsTruncated bool                   `gorm:"-" json:"binding_contributions_truncated,omitempty"`
	// Webhooks names the run-lifecycle hooks this agent's runs call, by the name
	// of a row in the webhook table (see WebhookM). It names them rather than
	// carrying their configuration so that moving an endpoint is one edit rather
	// than one per agent, and so a credential is never stored on this row.
	//
	// The names are unioned across every agent a run selects, like MCPs and
	// Skills, so an agent added to a conversation cannot silently drop the
	// admission check another one declared. Empty means this agent declares no
	// hooks of its own; the deployment default then applies, which is what keeps
	// an agent nobody configured from running unmetered.
	Webhooks StringList `gorm:"column:webhooks;type:text" json:"webhooks"`
	// AuthnWebhooks names authentication Webhooks (type=authentication) that
	// authenticate callers of this agent. Non-empty replaces the platform
	// authentication chain for this agent's call entries; AK/SK remains
	// platform-verified. Empty means platform authentication.
	AuthnWebhooks StringList `gorm:"column:authn_webhooks;type:text" json:"authn_webhooks"`
	// AuthzWebhooks names authorization Webhooks (type=authorization) that
	// decide whether a caller may use this agent. Non-empty replaces platform
	// authorization (including AuthzBackends) for this agent.
	AuthzWebhooks StringList `gorm:"column:authz_webhooks;type:text" json:"authz_webhooks"`
	// AuthzBackends, when non-empty, selects the subset of authorization
	// backends this agent is evaluated against at the Agent level (e.g.
	// ["opa"], or ["none"] to opt the agent out of Agent-level authorization
	// entirely). It is a JSON array string and is decoded by its reader, not by
	// the column: it is one of the few JSON-valued columns here that store is
	// written by hand rather than loaded through the store.
	AuthzBackends string `gorm:"type:text" json:"authz_backends"`
	// RuntimeProfile names the RuntimeProfile this agent runs on. Empty means
	// the deployment default profile (the in-process eino runtime).
	RuntimeProfile string `gorm:"column:runtime_profile;size:128;index" json:"runtime_profile"`
	// OutputSchema is the canonical JSON Schema a terminal answer must match.
	// Empty means the run has no structured-output contract.
	OutputSchema string `gorm:"type:text" json:"output_schema"`
	// OutputSchemaStrict asks a provider that supports strict decoding to
	// constrain generation.
	OutputSchemaStrict bool `json:"output_schema_strict"`
	// SessionTools grants the run the session capability: listing, reading,
	// continuing and forking the caller's own sessions. Read from the spec, not
	// stored in the table — like CustomCommands, the control plane owns it.
	SessionTools bool `gorm:"-" json:"session_tools,omitempty"`
	// MemoryRef names the Memory this agent uses. Empty disables memory.
	MemoryRef string `gorm:"-" json:"memory_ref,omitempty"`
	// MemoryEnabled is the resolved effective switch.
	MemoryEnabled bool `json:"memory_enabled"`
	// MemoryEmbeddingModel names the Model used for embeddings.
	MemoryEmbeddingModel string `gorm:"size:128" json:"memory_embedding_model"`
	// MemoryTTLDays bounds a memory's life. Zero uses the service default.
	MemoryTTLDays int32 `json:"memory_ttl_days"`
	// MemoryAutoSaveEvery reserves periodic summarization.
	MemoryAutoSaveEvery int32 `json:"memory_auto_save_every"`
	// Namespace is the business-line namespace this agent belongs to. Empty
	// means DefaultNamespace ("helper-public"). It gates namespace-level
	// authorization (see authz.ResourceKindNamespace).
	Namespace string    `gorm:"column:namespace;size:128;index" json:"namespace"`
	Labels    StringMap `gorm:"-" json:"labels"`
	CreatedAt int64     `json:"created_at"` // unix millis
	UpdatedAt int64     `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// BindingContributionM explains one AgentBinding's effect on an agent. It is
// read from Agent status, never stored in the table.
type BindingContributionM struct {
	Kind               string `json:"kind,omitempty"`
	Namespace          string `json:"namespace,omitempty"`
	Name               string `json:"name"`
	Generation         int64  `json:"generation,omitempty"`
	Hash               string `json:"hash,omitempty"`
	PromptCount        int32  `json:"prompt_count,omitempty"`
	SkillCount         int32  `json:"skill_count,omitempty"`
	MCPCount           int32  `json:"mcp_count,omitempty"`
	KnowledgeBaseCount int32  `json:"knowledge_base_count,omitempty"`
	CustomCommandCount int32  `json:"custom_command_count,omitempty"`
	CommandCount       int32  `json:"command_count,omitempty"`
}

// TableName returns the agent table name.
func (AgentM) TableName() string { return dbschema.Table(dbschema.Agent, "agent") }

// Enabled reports whether the agent is active.
func (a AgentM) Enabled() bool { return a.Status != StatusDisabled }

// EffectiveSkills returns what a run should use: the controller-resolved union
// (selector matches and aggregated agents included) when the status has been
// observed, otherwise the declared spec set.
func (a AgentM) EffectiveSkills() StringList {
	return resolvedOr(a.ResolvedSkills, a.Skills, a.Observed)
}

// EffectiveMCPs returns the controller-resolved MCP set, falling back to the
// declared one.
func (a AgentM) EffectiveMCPs() StringList {
	return resolvedOr(a.ResolvedMCPs, a.MCPs, a.Observed)
}

// EffectiveKnowledgeBases returns the controller-resolved knowledge-base set,
// falling back to the declared one.
func (a AgentM) EffectiveKnowledgeBases() StringList {
	return resolvedOr(a.ResolvedKnowledgeBases, a.KnowledgeBases, a.Observed)
}

// EffectiveWebhooks returns the controller-resolved hook set, falling back to
// the declared one. Inherited hooks travel with an aggregated agent's
// capabilities, so a run must not fall back to the declared set alone.
func (a AgentM) EffectiveWebhooks() StringList {
	return resolvedOr(a.ResolvedWebhooks, a.Webhooks, a.Observed)
}

// EffectiveRequireApproval returns the controller-resolved approval patterns,
// falling back to the declared ones.
func (a AgentM) EffectiveRequireApproval() StringList {
	return resolvedOr(a.ResolvedRequireApproval, a.RequireApproval, a.Observed)
}

// EffectiveCustomCommands returns the controller-resolved command set, falling
// back to the declared one.
func (a AgentM) EffectiveCustomCommands() StringList {
	return resolvedOr(a.ResolvedCustomCommands, a.CustomCommands, a.Observed)
}

// EffectiveCommands returns the binding-contributed chat commands.
func (a AgentM) EffectiveCommands() StringList {
	return resolvedOr(a.ResolvedCommands, nil, a.Observed)
}

// resolvedOr prefers the controller-resolved set once the controller has
// observed the object. Before that, only the declared set is trustworthy.
//
// The observed flag is what distinguishes "resolved to nothing" from "not
// resolved yet". Treating an empty resolved set as unobserved would resurrect
// the declared set after a binding is removed, which is the one failure mode
// that makes a revocation look like it worked while it did not.
func resolvedOr(resolved, declared StringList, observed bool) StringList {
	if observed {
		return resolved
	}
	return declared
}

// EffectiveTemplates returns the names of the AgentTemplates the controller
// expanded into this agent.
func (a AgentM) EffectiveTemplates() StringList {
	return resolvedOr(a.ResolvedTemplates, nil, a.Observed)
}

// EffectiveDelegates returns the controller-resolved delegate set, falling
// back to the declared one.
func (a AgentM) EffectiveDelegates() StringList {
	return resolvedOr(a.ResolvedDelegates, a.Delegates, a.Observed)
}

// EffectiveCalls returns the agents this agent may call as tools. A call may
// target a native or an external A2A agent; the runtime dispatches by the
// target's kind.
func (a AgentM) EffectiveCalls() StringList {
	return resolvedOr(a.ResolvedCalls, a.Calls, a.Observed)
}

// EffectiveSessionTools reports whether the agent or any of its templates
// opted into the session capability.
func (a AgentM) EffectiveSessionTools() bool {
	if a.Observed {
		return a.ResolvedSessionTools
	}
	return a.SessionTools
}

// EffectiveModel returns the controller-resolved default model, falling back to
// the declared one.
func (a AgentM) EffectiveModel() string {
	if a.Observed && a.ResolvedModel != "" {
		return a.ResolvedModel
	}
	return a.Model
}

// EffectiveOutputSchema returns the controller-resolved structured-output
// contract, falling back to the declared one.
func (a AgentM) EffectiveOutputSchema() (string, bool) {
	if a.Observed && a.ResolvedOutputSchema != "" {
		return a.ResolvedOutputSchema, a.ResolvedOutputSchemaStrict
	}
	return a.OutputSchema, a.OutputSchemaStrict
}

// MemorySettings is the effective long-term memory configuration.
type MemorySettings struct {
	Name           string
	Enabled        bool
	EmbeddingModel string
	TTLDays        int32
	AutoSaveEvery  int32
}

// EffectiveMemory returns the controller-resolved memory setting, falling back
// to the declared one.
func (a AgentM) EffectiveMemory() MemorySettings {
	if a.Observed && a.ResolvedMemoryEnabled {
		return MemorySettings{
			Name:           a.ResolvedMemoryName,
			Enabled:        true,
			EmbeddingModel: a.ResolvedMemoryEmbedding,
			TTLDays:        a.ResolvedMemoryTTLDays,
			AutoSaveEvery:  a.ResolvedMemoryAutoSave,
		}
	}
	return MemorySettings{
		Name:           a.MemoryRef,
		Enabled:        a.MemoryEnabled,
		EmbeddingModel: a.MemoryEmbeddingModel,
		TTLDays:        a.MemoryTTLDays,
		AutoSaveEvery:  a.MemoryAutoSaveEvery,
	}
}

// GetNamespace returns the normalized namespace.
func (a AgentM) GetNamespace() string { return NormalizeNamespace(a.Namespace) }
