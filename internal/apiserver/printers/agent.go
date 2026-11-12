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

package printers

import (
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/onexstack/onexai/pkg/apis/agent"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
)

// conditionLoaded is the condition an AuthorizationRule reports instead of the
// aggregate Ready one: its controller only resolves the rule's scope.
const conditionLoaded = "Loaded"

var (
	agentTable = resourceTable(
		func(obj *agent.Agent) []metav1.Condition { return obj.Status.Conditions },
		col(colOwner, func(obj *agent.Agent) any { return ownerName(obj.Spec.Owner) }),
		col(colDisplayNameWide, func(obj *agent.Agent) any { return orNone(obj.Spec.DisplayName) }),
	)

	authorizationRuleTable = resourceTableWith(
		conditionLoaded,
		func(obj *agent.AuthorizationRule) []metav1.Condition { return obj.Status.Conditions },
		col(colEffect, func(obj *agent.AuthorizationRule) any { return orNone(string(obj.Spec.Effect)) }),
	)

	// A ReferenceGrant has no status of its own; whether it takes effect is
	// reported by the referrer, so the columns describe the grant itself.
	referenceGrantTable = newConvertor(
		nameColumn[*agent.ReferenceGrant](),
		col(colFrom, func(obj *agent.ReferenceGrant) any { return describeGrantFrom(obj.Spec.From) }),
		col(colTo, func(obj *agent.ReferenceGrant) any { return describeGrantTo(obj.Spec.To) }),
		ageColumn[*agent.ReferenceGrant](),
	)

	knowledgeBaseTable = resourceTable(
		func(obj *agent.KnowledgeBase) []metav1.Condition { return obj.Status.Conditions },
		col(colPlatform, func(obj *agent.KnowledgeBase) any { return orNone(obj.Spec.Platform) }),
		col(colDatasetID, func(obj *agent.KnowledgeBase) any { return orNone(obj.Spec.DatasetID) }),
		col(colTopK, func(obj *agent.KnowledgeBase) any { return optInt32(obj.Spec.TopK) }),
		col(colScoreThreshold, func(obj *agent.KnowledgeBase) any { return optFloat64(obj.Spec.ScoreThreshold) }),
	)

	memoryTable = resourceTable(
		func(obj *agent.Memory) []metav1.Condition { return obj.Status.Conditions },
		col(colEnabled, func(obj *agent.Memory) any { return strconv.FormatBool(obj.Spec.Enabled) }),
		col(colModel, func(obj *agent.Memory) any {
			if obj.Spec.Embedding.ModelRef == nil {
				return orNone("")
			}
			return orNone(obj.Spec.Embedding.ModelRef.Name)
		}),
		col(colDimensions, func(obj *agent.Memory) any { return obj.Spec.Embedding.Dimensions }),
		col(colScope, func(obj *agent.Memory) any { return orNone(obj.Spec.Scope.SubjectMode) }),
		col(colTopK, func(obj *agent.Memory) any { return obj.Spec.Retrieval.TopK }),
	)

	mcpServerTable = resourceTable(
		func(obj *agent.MCPServer) []metav1.Condition { return obj.Status.Conditions },
		col(colProtocol, func(obj *agent.MCPServer) any { return orNone(obj.Spec.Protocol) }),
	)

	modelTable = resourceTable(
		func(obj *agent.Model) []metav1.Condition { return obj.Status.Conditions },
		col(colModel, func(obj *agent.Model) any { return orNone(obj.Spec.Model) }),
	)

	promptTable = resourceTable(
		func(obj *agent.Prompt) []metav1.Condition { return obj.Status.Conditions },
	)

	skillTable = resourceTable(
		func(obj *agent.Skill) []metav1.Condition { return obj.Status.Conditions },
		col(colMode, func(obj *agent.Skill) any { return orNone(obj.Spec.Mode) }),
		col(colProvider, func(obj *agent.Skill) any { return orNone(obj.Spec.Provider) }),
	)

	// A custom command has no controller of its own, so a STATUS column would be
	// permanently Unknown. Its own shape is what a reader wants instead.
	customCommandTable = newConvertor(
		nameColumn[*agent.CustomCommand](),
		col(colType, func(obj *agent.CustomCommand) any { return orNone(obj.Spec.Type) }),
		col(colEnabled, func(obj *agent.CustomCommand) any {
			return strconv.FormatBool(obj.Spec.Enabled)
		}),
		col(colParameters, func(obj *agent.CustomCommand) any { return len(obj.Spec.Parameters) }),
		col(colAliases, func(obj *agent.CustomCommand) any { return strings.Join(obj.Spec.Aliases, ",") }),
		ageColumn[*agent.CustomCommand](),
	)

	webhookTable = resourceTable(
		func(obj *agent.Webhook) []metav1.Condition { return obj.Status.Conditions },
	)

	runtimeProfileTable = resourceTable(
		func(obj *agent.RuntimeProfile) []metav1.Condition { return obj.Status.Conditions },
		col(colKind, func(obj *agent.RuntimeProfile) any { return orNone(obj.Spec.Kind) }),
		col(colEndpoint, func(obj *agent.RuntimeProfile) any {
			if obj.Status.ResolvedEndpoint != "" {
				return obj.Status.ResolvedEndpoint
			}
			return orNone(obj.Spec.Endpoint)
		}),
	)

	runtimeHostingTable = resourceTable(
		func(obj *agent.RuntimeHosting) []metav1.Condition { return obj.Status.Conditions },
		col(colProfile, func(obj *agent.RuntimeHosting) any { return orNone(obj.Spec.ProfileRef.Name) }),
		col(colInstances, func(obj *agent.RuntimeHosting) any { return len(obj.Status.Instances) }),
	)

	sandboxTemplateTable = resourceTable(
		func(obj *agent.SandboxTemplate) []metav1.Condition { return obj.Status.Conditions },
		col(colImage, func(obj *agent.SandboxTemplate) any { return orNone(obj.Spec.Image) }),
	)

	// A template has no controller of its own, so STATUS would be permanently
	// Unknown. What a reader wants is its shape: whether it is live, its default
	// model, and how many skills it contributes.
	agentTemplateTable = newConvertor(
		nameColumn[*agent.AgentTemplate](),
		col(colEnabled, func(obj *agent.AgentTemplate) any {
			return strconv.FormatBool(obj.Spec.Enabled)
		}),
		col(colModel, func(obj *agent.AgentTemplate) any {
			if obj.Spec.ModelRef == nil {
				return orNone("")
			}
			return orNone(obj.Spec.ModelRef.Name)
		}),
		col(colSkills, func(obj *agent.AgentTemplate) any { return len(obj.Spec.Skills) }),
		ageColumn[*agent.AgentTemplate](),
	)

	scheduledRunTable = resourceTable(
		func(obj *agent.ScheduledRun) []metav1.Condition { return obj.Status.Conditions },
		col(colAgent, func(obj *agent.ScheduledRun) any { return orNone(obj.Spec.Agent.Name) }),
		col(colSchedule, func(obj *agent.ScheduledRun) any { return orNone(obj.Spec.Schedule) }),
		col(colPaused, func(obj *agent.ScheduledRun) any {
			return strconv.FormatBool(obj.Spec.Paused)
		}),
	)

	// A binding has no controller-owned Ready condition of its own beyond the
	// shared set, so STATUS comes from the aggregate Ready condition the binding
	// controller writes.
	agentBindingTable = resourceTable(
		func(obj *agent.AgentBinding) []metav1.Condition { return obj.Status.Conditions },
		col(colEnabled, func(obj *agent.AgentBinding) any {
			return strconv.FormatBool(obj.Spec.Enabled == nil || *obj.Spec.Enabled)
		}),
		col(colSelector, func(obj *agent.AgentBinding) any { return selectorString(obj.Spec.Selector) }),
		col(colMatchedAgents, func(obj *agent.AgentBinding) any { return obj.Status.MatchedAgents }),
		col(colSkills, func(obj *agent.AgentBinding) any { return len(obj.Status.Resolved.Skills) }),
		col(colMCPs, func(obj *agent.AgentBinding) any { return len(obj.Status.Resolved.MCPs) }),
		col(colKBs, func(obj *agent.AgentBinding) any { return len(obj.Status.Resolved.KnowledgeBases) }),
		col(colCommands, func(obj *agent.AgentBinding) any { return len(obj.Status.Resolved.Commands) }),
	)
)

// clusterAgentBindingTable is the table for cluster-wide bindings. It shows the
// tenant count rather than a selector, because the audience is an explicit list.
var (
	clusterAgentBindingTable = resourceTable(
		func(obj *agent.ClusterAgentBinding) []metav1.Condition { return obj.Status.Conditions },
		col(colEnabled, func(obj *agent.ClusterAgentBinding) any {
			return strconv.FormatBool(obj.Spec.Enabled == nil || *obj.Spec.Enabled)
		}),
		col(colNamespaces, func(obj *agent.ClusterAgentBinding) any {
			if agentvalidation.IsAllNamespaces(obj.Spec.Namespaces) {
				return agent.AllNamespaces + " (all)"
			}
			return len(obj.Spec.Namespaces)
		}),
		col(colMatchedAgents, func(obj *agent.ClusterAgentBinding) any { return obj.Status.MatchedAgents }),
		col(colSkills, func(obj *agent.ClusterAgentBinding) any { return len(obj.Status.Resolved.Skills) }),
		col(colMCPs, func(obj *agent.ClusterAgentBinding) any { return len(obj.Status.Resolved.MCPs) }),
		col(colKBs, func(obj *agent.ClusterAgentBinding) any { return len(obj.Status.Resolved.KnowledgeBases) }),
		col(colCommands, func(obj *agent.ClusterAgentBinding) any { return len(obj.Status.Resolved.Commands) }),
	)
)

// Agents returns the table convertor for agents.
func Agents() rest.TableConvertor { return agentTable }

// ClusterAgentBindings returns the table convertor for clusteragentbindings.
func ClusterAgentBindings() rest.TableConvertor { return clusterAgentBindingTable }

// AgentTemplates returns the table convertor for agent templates.
func AgentTemplates() rest.TableConvertor { return agentTemplateTable }

// AgentBindings returns the table convertor for agentbindings.
func AgentBindings() rest.TableConvertor { return agentBindingTable }

// CustomCommands returns the table convertor for customcommands.
func CustomCommands() rest.TableConvertor { return customCommandTable }

// AuthorizationRules returns the table convertor for authorizationrules.
func AuthorizationRules() rest.TableConvertor { return authorizationRuleTable }

// ReferenceGrants returns the table convertor for referencegrants.
func ReferenceGrants() rest.TableConvertor { return referenceGrantTable }

// KnowledgeBases returns the table convertor for knowledgebases.
func KnowledgeBases() rest.TableConvertor { return knowledgeBaseTable }

// Memories returns the table convertor for memories.
func Memories() rest.TableConvertor { return memoryTable }

// MCPServers returns the table convertor for mcpservers.
func MCPServers() rest.TableConvertor { return mcpServerTable }

// Models returns the table convertor for models.
func Models() rest.TableConvertor { return modelTable }

// Prompts returns the table convertor for prompts.
func Prompts() rest.TableConvertor { return promptTable }

// Skills returns the table convertor for skills.
func Skills() rest.TableConvertor { return skillTable }

// Webhooks returns the table convertor for webhooks.
func Webhooks() rest.TableConvertor { return webhookTable }

// RuntimeProfiles returns the table convertor for runtime profiles.
func RuntimeProfiles() rest.TableConvertor { return runtimeProfileTable }

// RuntimeHostings returns the table convertor for runtime hostings.
func RuntimeHostings() rest.TableConvertor { return runtimeHostingTable }

// ScheduledRuns returns the table convertor for scheduled runs.
func ScheduledRuns() rest.TableConvertor { return scheduledRunTable }

// SandboxTemplates returns the table convertor for sandbox templates.
func SandboxTemplates() rest.TableConvertor { return sandboxTemplateTable }

// describeGrantFrom renders the referrer side of a grant for kubectl.
func describeGrantFrom(from []agent.ReferenceGrantFrom) string {
	parts := make([]string, 0, len(from))
	for _, f := range from {
		parts = append(parts, f.Namespace+"/"+f.Kind)
	}
	return strings.Join(parts, ",")
}

// describeGrantTo renders the target side of a grant for kubectl. An empty
// name is shown as "*" so a namespace-wide grant is visible rather than
// looking like a malformed entry.
func describeGrantTo(to []agent.ReferenceGrantTo) string {
	parts := make([]string, 0, len(to))
	for _, t := range to {
		name := t.Name
		if name == "" {
			name = "*"
		}
		parts = append(parts, t.Kind+"/"+name)
	}
	return strings.Join(parts, ",")
}
