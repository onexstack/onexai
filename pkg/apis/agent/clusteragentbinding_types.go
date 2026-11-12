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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AllNamespaces is the wildcard entry for ClusterAgentBindingSpec.Namespaces.
// It must be the only entry in the list; see the v1alpha1 definition for why.
const AllNamespaces = "*"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterAgentBinding is the internal (hub) schema for the
// clusteragentbindings API.
type ClusterAgentBinding struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   ClusterAgentBindingSpec
	Status ClusterAgentBindingStatus
}

// ClusterAgentBindingSpec defines the desired state of ClusterAgentBinding.
type ClusterAgentBindingSpec struct {
	Enabled     *bool
	DisplayName string
	Description string
	Owner       *rbacv1.Subject
	// Namespaces is the audience's namespace list. A single AllNamespaces
	// entry means every namespace, including ones created later.
	Namespaces     []string
	AgentSelector  *metav1.LabelSelector
	Agents         []Reference
	Prompts        []Reference
	Skills         []Reference
	KnowledgeBases []Reference
	MCPServers     []Reference
	CustomCommands []Reference
	// CommandSelector selects catalogue commands by label, unioned with
	// Commands. See the versioned type for the empty-selector rule.
	CommandSelector *metav1.LabelSelector
	Commands        []string
	MemoryRef       *Reference
	ModelRef        *Reference
}

// ClusterAgentBindingStatus defines the observed state of ClusterAgentBinding.
type ClusterAgentBindingStatus struct {
	CommonStatus

	MatchedNamespaces int32
	MatchedAgents     int32
	Resolved          AgentBindingResolved
	Missing           []string
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterAgentBindingList is a list of ClusterAgentBinding objects.
type ClusterAgentBindingList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []ClusterAgentBinding
}
