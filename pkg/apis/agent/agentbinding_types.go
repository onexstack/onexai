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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentBinding is the internal (hub) schema for the AgentBinding API.
type AgentBinding struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   AgentBindingSpec
	Status AgentBindingStatus
}

// AgentBindingSpec defines the desired state of AgentBinding.
type AgentBindingSpec struct {
	Enabled        *bool
	DisplayName    string
	Description    string
	Owner          *rbacv1.Subject
	Selector       *metav1.LabelSelector
	Agents         []string
	ModelRef       *Reference
	Prompts        []Reference
	Skills         []Reference
	KnowledgeBases []Reference
	MCPServers     []Reference
	CustomCommands []Reference
	// CommandSelector selects catalogue commands by label, unioned with
	// Commands. See the versioned type for the empty-selector rule.
	CommandSelector *metav1.LabelSelector
	Commands        []string
}

// AgentBindingStatus defines the observed state of AgentBinding.
type AgentBindingStatus struct {
	CommonStatus

	MatchedAgents int32
	Resolved      AgentBindingResolved
	Missing       []string
}

// AgentBindingResolved is what one binding contributes.
type AgentBindingResolved struct {
	Prompts        []string
	Skills         []string
	MCPs           []string
	KnowledgeBases []string
	CustomCommands []string
	Commands       []string
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentBindingList is a list of AgentBinding objects.
type AgentBindingList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []AgentBinding
}
