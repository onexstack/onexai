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

// This file holds the request and response types for the agent-binding
// endpoints that do not have a protobuf definition. They live beside the
// generated API types so the handler and the biz layer exchange the same
// currency, as they do for the protobuf-backed endpoints.

package v1

import agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"

// PreviewAgentBindingRequest is the dry-run input. The spec is the same shape a
// real AgentBinding carries, so a manifest can be posted after stripping its
// envelope.
type PreviewAgentBindingRequest struct {
	Namespace string                         `json:"namespace"`
	Spec      agentv1alpha1.AgentBindingSpec `json:"spec"`
}

// PreviewAgentBindingResponse is the dry-run answer: which agents the binding
// selects and what its references resolve to.
type PreviewAgentBindingResponse struct {
	MatchedAgents []string                    `json:"matchedAgents"`
	Resolved      PreviewAgentBindingResolved `json:"resolved"`
	Missing       []string                    `json:"missing"`
}

// PreviewAgentBindingResolved is the set of references that resolved, by kind.
type PreviewAgentBindingResolved struct {
	Prompts        []string `json:"prompts"`
	Skills         []string `json:"skills"`
	MCPs           []string `json:"mcps"`
	KnowledgeBases []string `json:"knowledgeBases"`
	CustomCommands []string `json:"customCommands"`
	Commands       []string `json:"commands"`
}

// GetEffectiveAgentResponse answers "what does this agent actually have, and
// where did each item come from".
type GetEffectiveAgentResponse struct {
	Agent        string                     `json:"agent"`
	Observed     bool                       `json:"observed"`
	ResolvedHash string                     `json:"resolvedHash"`
	Capabilities EffectiveAgentCapabilities `json:"capabilities"`
	Bindings     EffectiveAgentBindings     `json:"bindings"`
	// Unresolved lists the references the controller could not resolve, with
	// the reason in parentheses.
	Unresolved []string `json:"unresolved,omitempty"`
}

// EffectiveAgentCapabilities is the controller-resolved capability set a run
// uses, falling back to the declared set before the controller has observed the
// agent.
type EffectiveAgentCapabilities struct {
	Model           string   `json:"model,omitempty"`
	Skills          []string `json:"skills"`
	MCPs            []string `json:"mcps"`
	KnowledgeBases  []string `json:"knowledgeBases"`
	CustomCommands  []string `json:"customCommands"`
	Commands        []string `json:"commands"`
	RequireApproval []string `json:"requireApproval"`
	Webhooks        []string `json:"webhooks"`
	Templates       []string `json:"templates"`
	Delegates       []string `json:"delegates"`
	Calls           []string `json:"calls"`
	SessionTools    bool     `json:"sessionTools"`
}

// EffectiveAgentBindings names the AgentBindings that selected the agent.
type EffectiveAgentBindings struct {
	Total     int32                        `json:"total"`
	Truncated bool                         `json:"truncated"`
	Items     []EffectiveAgentContribution `json:"items"`
}

// EffectiveAgentContribution is one binding's contribution to an agent.
type EffectiveAgentContribution struct {
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
