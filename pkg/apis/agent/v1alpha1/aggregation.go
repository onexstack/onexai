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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

const (
	// AggregatedPromptPrefix prefixes the derived Prompt that holds an agent's
	// aggregated prompt.
	AggregatedPromptPrefix = "aggregated-"

	// InlinePromptPrefix prefixes the Prompt the SQL->CRD migration (pg2crd)
	// creates from legacy inline sysPrompt text.
	InlinePromptPrefix = "inline-"

	// BuiltinPrompt is the sentinel meaning "no prompt override"; the engine's
	// default instruction applies.
	BuiltinPrompt = "__builtin__"

	// AnnotationAggregatedFor marks a derived aggregated Prompt and records
	// the agent it belongs to. It is also the marker that keeps one agent's
	// aggregate from being picked up as another agent's source.
	AnnotationAggregatedFor = "onexai.io/aggregated-for"

	// AnnotationAggregatedContentHash is the digest of the content the
	// controller last wrote (enabled + template + variables). A mismatch means
	// the object was edited outside the controller.
	AnnotationAggregatedContentHash = "onexai.io/aggregated-content-hash"

	// AnnotationAggregatedInputHash is the digest of the inputs that produced
	// the content the controller last wrote.
	AnnotationAggregatedInputHash = "onexai.io/aggregated-input-hash"

	// AnnotationInlineFor marks a Prompt materialized from a legacy inline
	// prompt and records the agent it belongs to.
	AnnotationInlineFor = "onexai.io/inline-for"
)

// AggregatedPromptName returns the name of the derived aggregated Prompt for
// an agent.
func AggregatedPromptName(agentName string) string {
	return AggregatedPromptPrefix + agentName
}

// InlinePromptName returns the name of the Prompt migrated from an agent's
// legacy inline prompt.
func InlinePromptName(agentName string) string {
	return InlinePromptPrefix + agentName
}

// AgentOwnerReference builds the controller ownerReference a derived Prompt
// carries so the garbage collector removes it with its agent.
func AgentOwnerReference(agentName string, agentUID types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         SchemeGroupVersion.String(),
		Kind:               "Agent",
		Name:               agentName,
		UID:                agentUID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}
