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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Column definitions. kubectl upper-cases the name before printing it, so the
// spelling here only decides where the word breaks are.
var (
	colName = metav1.TableColumnDefinition{
		Name:        "Name",
		Type:        "string",
		Format:      "name",
		Description: "Name must be unique within a namespace.",
	}
	colStatus = metav1.TableColumnDefinition{
		Name:        "Status",
		Type:        "string",
		Description: "Aggregate readiness: Ready, NotReady, Unknown, or Terminating while being deleted.",
	}
	colAge = metav1.TableColumnDefinition{
		Name:        "Age",
		Type:        "string",
		Description: "Time elapsed since the object was created.",
	}

	colOwner = metav1.TableColumnDefinition{
		Name:        "Owner",
		Type:        "string",
		Description: "Name of the declarative owner (spec.owner.name).",
	}
	colDisplayName = metav1.TableColumnDefinition{
		Name:        "Display Name",
		Type:        "string",
		Description: "Human-facing name (spec.displayName).",
	}
	colDisplayNameWide = metav1.TableColumnDefinition{
		Name:        "Display Name",
		Type:        "string",
		Priority:    1,
		Description: "Human-facing name (spec.displayName).",
	}

	colEffect = metav1.TableColumnDefinition{
		Name:        "Effect",
		Type:        "string",
		Description: "Outcome the rule asks for when it matches (spec.effect).",
	}

	colFrom = metav1.TableColumnDefinition{
		Name:        "From",
		Type:        "string",
		Description: "Referrers this grant speaks about (namespace/kind).",
	}
	colTo = metav1.TableColumnDefinition{
		Name:        "To",
		Type:        "string",
		Description: "Targets this grant exposes (kind/name; kind/* means every name).",
	}

	colPlatform = metav1.TableColumnDefinition{
		Name:        "Platform",
		Type:        "string",
		Description: "Knowledge base backend (spec.platform).",
	}
	colDatasetID = metav1.TableColumnDefinition{
		Name:        "Dataset ID",
		Type:        "string",
		Description: "Dataset identifier on the backend (spec.datasetID).",
	}
	colTopK = metav1.TableColumnDefinition{
		Name:        "Top K",
		Type:        "string",
		Priority:    1,
		Description: "Number of chunks retrieval asks for (spec.topK).",
	}
	colScoreThreshold = metav1.TableColumnDefinition{
		Name:        "Score Threshold",
		Type:        "string",
		Priority:    1,
		Description: "Minimum retrieval score a chunk must reach (spec.scoreThreshold).",
	}

	colProtocol = metav1.TableColumnDefinition{
		Name:        "Protocol",
		Type:        "string",
		Description: "Transport used to reach the server (spec.protocol).",
	}

	colModel = metav1.TableColumnDefinition{
		Name:        "Model",
		Type:        "string",
		Description: "Upstream model name (spec.model).",
	}
	colDimensions = metav1.TableColumnDefinition{
		Name:        "Dimensions",
		Type:        "integer",
		Description: "Embedding vector dimension (spec.embedding.dimensions).",
	}
	colScope = metav1.TableColumnDefinition{
		Name:        "Scope",
		Type:        "string",
		Description: "Memory bucket mode (spec.scope.subjectMode).",
	}

	colKind = metav1.TableColumnDefinition{
		Name:        "Kind",
		Type:        "string",
		Description: "Runtime adapter (spec.kind).",
	}
	colEndpoint = metav1.TableColumnDefinition{
		Name:        "Endpoint",
		Type:        "string",
		Description: "A2A endpoint the gateway dials (status.resolvedEndpoint).",
	}
	colImage = metav1.TableColumnDefinition{
		Name:        "Image",
		Type:        "string",
		Description: "Digest-pinned container image (spec.image).",
	}
	colProfile = metav1.TableColumnDefinition{
		Name:        "Profile",
		Type:        "string",
		Description: "Runtime profile the hosting serves (spec.profileRef).",
	}
	colAgent = metav1.TableColumnDefinition{
		Name:        "Agent",
		Type:        "string",
		Description: "Agent the schedule runs (spec.agent).",
	}
	colSchedule = metav1.TableColumnDefinition{
		Name:        "Schedule",
		Type:        "string",
		Description: "Five-field cron expression (spec.schedule).",
	}
	colPaused = metav1.TableColumnDefinition{
		Name:        "Paused",
		Type:        "string",
		Description: "Whether new occurrences are suspended (spec.paused).",
	}
	colInstances = metav1.TableColumnDefinition{
		Name:        "Instances",
		Type:        "integer",
		Description: "Runtime instances currently known to the pool (status.instances).",
	}

	colMode = metav1.TableColumnDefinition{
		Name:        "Mode",
		Type:        "string",
		Description: "How the skill is executed (spec.mode).",
	}
	colType = metav1.TableColumnDefinition{
		Name:        "Type",
		Type:        "string",
		Description: "Execution body of a custom command (spec.type).",
	}
	colEnabled = metav1.TableColumnDefinition{
		Name:        "Enabled",
		Type:        "string",
		Description: "Whether the object takes part in resolution (spec.enabled).",
	}
	colParameters = metav1.TableColumnDefinition{
		Name:        "Params",
		Type:        "integer",
		Description: "Number of declared parameters (spec.parameters).",
	}
	colAliases = metav1.TableColumnDefinition{
		Name:        "Aliases",
		Type:        "string",
		Priority:    1,
		Description: "Extra command names (spec.aliases).",
	}
	colProvider = metav1.TableColumnDefinition{
		Name:        "Provider",
		Type:        "string",
		Description: "Upstream provider (spec.provider).",
	}

	colAppID = metav1.TableColumnDefinition{
		Name:        "App ID",
		Type:        "string",
		Description: "Feishu application identifier (spec.appID).",
	}
	colBotName = metav1.TableColumnDefinition{
		Name:        "Bot Name",
		Type:        "string",
		Description: "Bot display name on the IM platform (spec.botName).",
	}
	colEventMode = metav1.TableColumnDefinition{
		Name:        "Event Mode",
		Type:        "string",
		Priority:    1,
		Description: "How inbound events are delivered (spec.eventMode).",
	}

	colAgents = metav1.TableColumnDefinition{
		Name:        "Agents",
		Type:        "string",
		Description: "Agents the object applies to (spec.agents).",
	}
	colCanonical = metav1.TableColumnDefinition{
		Name:        "Canonical",
		Type:        "string",
		Description: "Canonical peer identity (spec.canonical).",
	}
	colPeerID = metav1.TableColumnDefinition{
		Name:        "Peer ID",
		Type:        "string",
		Description: "Peer identifier on the backend (spec.peerID).",
	}

	colSelector = metav1.TableColumnDefinition{
		Name:        "Selector",
		Type:        "string",
		Priority:    1,
		Description: "Label selector that picks the agents (spec.selector).",
	}
	colMatchedAgents = metav1.TableColumnDefinition{
		Name:        "Agents",
		Type:        "integer",
		Description: "Number of agents the binding currently selects (status.matchedAgents).",
	}
	colNamespaces = metav1.TableColumnDefinition{
		Name:        "Namespaces",
		Type:        "integer",
		Priority:    1,
		Description: "Number of target namespaces this cluster binding declares (spec.namespaces).",
	}
	colSkills = metav1.TableColumnDefinition{
		Name:        "Skills",
		Type:        "integer",
		Priority:    1,
		Description: "Number of skills the binding contributes (status.resolved.skills).",
	}
	colMCPs = metav1.TableColumnDefinition{
		Name:        "MCPs",
		Type:        "integer",
		Priority:    1,
		Description: "Number of MCP servers the binding contributes (status.resolved.mcps).",
	}
	colKBs = metav1.TableColumnDefinition{
		Name:        "KBs",
		Type:        "integer",
		Priority:    1,
		Description: "Number of knowledge bases the binding contributes (status.resolved.knowledgeBases).",
	}
	colGroup = metav1.TableColumnDefinition{
		Name:        "Group",
		Type:        "string",
		Description: "Catalogue group the command is listed under (spec.group).",
	}
	colUsage = metav1.TableColumnDefinition{
		Name:        "Usage",
		Type:        "string",
		Priority:    1,
		Description: "Human-facing invocation shape (spec.usage).",
	}
	colCommands = metav1.TableColumnDefinition{
		Name:        "Commands",
		Type:        "integer",
		Priority:    1,
		Description: "Number of chat commands the binding contributes (status.resolved.commands).",
	}
)

// col builds a cell from an arbitrary function of the object. It is the escape
// hatch for resource-specific fields; the shared shapes have named helpers.
func col[T object](def metav1.TableColumnDefinition, cell func(T) any) column[T] {
	return column[T]{
		def:  def,
		cell: func(obj T, _ time.Time) any { return cell(obj) },
	}
}

// nameColumn is the NAME column every table starts with.
func nameColumn[T object]() column[T] {
	return column[T]{
		def:  colName,
		cell: func(obj T, _ time.Time) any { return obj.GetName() },
	}
}

// statusColumn derives STATUS from the object's conditions. Deletion wins over
// whatever the conditions still say.
func statusColumn[T object](conditions func(T) []metav1.Condition, conditionType string) column[T] {
	return col(colStatus, func(obj T) any {
		if !obj.GetDeletionTimestamp().IsZero() {
			return terminating
		}

		return conditionStatus(conditions(obj), conditionType)
	})
}

// ageColumn is the AGE column every table ends with.
func ageColumn[T object]() column[T] {
	return column[T]{
		def: colAge,
		cell: func(obj T, now time.Time) any {
			return humanAge(obj.GetCreationTimestamp(), now)
		},
	}
}

// resourceTable builds a table that starts with NAME / STATUS and ends with
// AGE, with the resource's own columns in between. That is the shape of every
// resource in both groups, so the skeleton lives in one place.
func resourceTable[T object](conditions func(T) []metav1.Condition, fields ...column[T]) *convertor[T] {
	return resourceTableWith(conditionReady, conditions, fields...)
}

// resourceTableWith is resourceTable for a resource that reports a condition
// type other than Ready as its status.
func resourceTableWith[T object](conditionType string, conditions func(T) []metav1.Condition, fields ...column[T]) *convertor[T] {
	columns := make([]column[T], 0, len(fields)+3)
	columns = append(columns, nameColumn[T](), statusColumn(conditions, conditionType))
	columns = append(columns, fields...)
	columns = append(columns, ageColumn[T]())

	return newConvertor(columns...)
}
