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

package agent

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentTemplate is the internal (hub) schema for reusable agent behavior.
type AgentTemplate struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   AgentTemplateSpec
	Status AgentTemplateStatus
}

// AgentTemplateSpec defines the behavior a template contributes.
type AgentTemplateSpec struct {
	CommonSpec

	PromptRef             *Reference
	Prompts               []Reference
	PromptSelector        *metav1.LabelSelector
	ModelRef              *Reference
	Skills                []Reference
	SkillSelector         *metav1.LabelSelector
	MCPs                  []Reference
	MCPServerSelector     *metav1.LabelSelector
	KnowledgeBases        []Reference
	KnowledgeBaseSelector *metav1.LabelSelector
	CustomCommands        []Reference
	CustomCommandSelector *metav1.LabelSelector
	Webhooks              []string
	RequireApproval       []string
	Delegates             []Reference
	OutputSchema          *apiextensionsv1.JSON
	OutputSchemaStrict    bool
	SessionTools          bool
	MemoryRef             *Reference
}

// AgentTemplateStatus is the observed state of the template.
type AgentTemplateStatus struct {
	CommonStatus
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentTemplateList is a list of AgentTemplate objects.
type AgentTemplateList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []AgentTemplate
}
