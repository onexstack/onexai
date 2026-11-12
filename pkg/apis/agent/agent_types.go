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

package agent

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Agent is the internal (hub) schema for the agents API.
type Agent struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   AgentSpec
	Status AgentStatus
}

// AgentSpec defines the desired state of Agent.
type AgentSpec struct {
	CommonSpec

	Kind      AgentKind
	Execution *AgentExecution
	Contract  *AgentContract
	Sharing   *AgentSharing
	Calls     []Reference

	PromptRef         *Reference
	Prompts           []Reference
	PromptSelector    *metav1.LabelSelector
	PromptAggregation *PromptAggregationSpec
	// TemplateRefs names AgentTemplates whose behavior this agent inherits.
	// The list is ordered; scalar fields fall back in that order.
	TemplateRefs  []Reference
	Agents        []Reference
	AgentSelector *metav1.LabelSelector
	// Delegates names the agents this agent may call as tools. It is not
	// aggregation: a delegate runs as its own run, with its own prompt, session
	// and runtime, and only the name crosses the boundary. Empty means the agent
	// cannot delegate at all.
	Delegates             []Reference
	ModelRef              *Reference
	Skills                []Reference
	SkillSelector         *metav1.LabelSelector
	MCPs                  []Reference
	MCPServerSelector     *metav1.LabelSelector
	KnowledgeBases        []Reference
	KnowledgeBaseSelector *metav1.LabelSelector
	Webhooks              []string
	AuthnWebhooks         []string
	AuthzWebhooks         []string
	RequireApproval       []string
	AuthzBackends         []string
	MaxSkills             int32
	CustomCommands        []Reference
	CustomCommandSelector *metav1.LabelSelector
	// Runtime selects the runtime contract. Nil means the deployment default
	// profile, which is the in-process eino runtime.
	Runtime *AgentRuntimeSpec
	// OutputSchema constrains the successful terminal answer when this agent is
	// the root of a run.
	OutputSchema *apiextensionsv1.JSON
	// OutputSchemaStrict asks providers that support strict schema generation to
	// constrain decoding. The gateway validates the answer regardless.
	OutputSchemaStrict bool
	// SessionTools grants the run the session capability.
	SessionTools bool
	// MemoryRef names the Memory this agent uses. Nil disables long-term
	// memory; there is no implicit default.
	MemoryRef *Reference
}

// AgentMemorySpec is the controller-resolved effective memory configuration.
type AgentMemorySpec struct {
	Namespace      string
	Name           string
	Enabled        bool
	EmbeddingModel string
	TTLDays        int32
	AutoSaveEvery  int32
}

// AgentRuntimeSpec selects a RuntimeProfile for an agent. The profile is the
// only runtime selector: its name is the runtime, so there is no second type
// enum to disagree with it.
type AgentRuntimeSpec struct {
	// ProfileRef names a RuntimeProfile in the agent's namespace.
	ProfileRef *Reference
}

// PromptAggregationSpec controls the derived aggregated prompt.
type PromptAggregationSpec struct {
	RegenerateToken string
}

// AgentStatus defines the observed state of Agent.
type AgentStatus struct {
	CommonStatus

	Resolved                      AgentResolved
	ResolvedHash                  string
	Missing                       []string
	A2A                           *AgentA2AStatus
	BindingContributions          []AgentBindingContribution
	BindingContributionsTotal     int32
	BindingContributionsTruncated bool
	AggregatedPrompt              string
	AggregatedInputHash           string
	AggregatedPromptHash          string
	AggregatedPromptOverridden    bool
	AggregatedPromptStale         bool
	ObservedRegenerateToken       string
}

// AgentResolved is what an agent's spec resolves to at the observed generation.
type AgentResolved struct {
	Prompt             string
	Prompts            []string
	Agents             []string
	Templates          []string
	Model              string
	ModelNamespace     string
	Skills             []string
	MCPs               []string
	KnowledgeBases     []string
	Webhooks           []string
	RequireApproval    []string
	CustomCommands     []string
	Commands           []string
	Delegates          []string
	Calls              []string
	SessionTools       bool
	OutputSchema       *apiextensionsv1.JSON
	OutputSchemaStrict bool
	Memory             *AgentMemorySpec
}

// AgentBindingContribution records one binding's effect on an agent.
type AgentBindingContribution struct {
	Kind               string
	Namespace          string
	Name               string
	Generation         int64
	Hash               string
	PromptCount        int32
	SkillCount         int32
	MCPCount           int32
	KnowledgeBaseCount int32
	CustomCommandCount int32
	CommandCount       int32
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentList is a list of Agent objects.
type AgentList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []Agent
}
