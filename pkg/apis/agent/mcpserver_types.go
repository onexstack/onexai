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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MCPServer is the internal (hub) schema for the mcpservers API.
type MCPServer struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   MCPServerSpec
	Status MCPServerStatus
}

// MCPServerSpec defines the desired state of MCPServer.
type MCPServerSpec struct {
	CommonSpec

	Protocol     string
	URL          string
	ClientConfig *ClientConfig
	Command      []string
	HeadersRef *corev1.LocalObjectReference
	AllowedTools []string
	RequireApproval   []string
	ResourceMappings  *runtime.RawExtension
}

// MCPServerStatus defines the observed state of MCPServer.
type MCPServerStatus struct {
	CommonStatus

	Tools            []MCPTool
	LastDiscoverTime *metav1.Time
}

// MCPTool describes one tool a server exposes.
type MCPTool struct {
	Name        string
	Description string
	SchemaHash  string
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MCPServerList is a list of MCPServer objects.
type MCPServerList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []MCPServer
}
