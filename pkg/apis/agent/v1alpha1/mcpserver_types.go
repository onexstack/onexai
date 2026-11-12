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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MCPServer is one MCP endpoint. The controller performs the handshake and
// publishes the discovered tools in status, so a request never pays a dial.
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec MCPServerSpec `json:"spec,omitempty"`
	// +optional
	Status MCPServerStatus `json:"status,omitempty"`
}

// MCPServerSpec defines the desired state of MCPServer.
type MCPServerSpec struct {
	CommonSpec `json:",inline"`

	// Protocol is streamable-http, sse or stdio.
	Protocol string `json:"protocol"`

	// URL is the endpoint for the HTTP protocols.
	// +optional
	URL string `json:"url,omitempty"`

	// ClientConfig is the shared Kubernetes-shaped endpoint configuration. When
	// set it replaces URL; headersRef still contributes static headers.
	// +optional
	ClientConfig *ClientConfig `json:"clientConfig,omitempty"`

	// Command is the argv for the stdio protocol.
	// +optional
	Command []string `json:"command,omitempty"`

	// HeadersRef names a Secret in this namespace whose data are the HTTP
	// headers sent to the server.
	// +optional
	HeadersRef *corev1.LocalObjectReference `json:"headersRef,omitempty"`

	// AllowedTools is the tool whitelist; empty means every tool the server
	// exposes.
	// +optional
	AllowedTools []string `json:"allowedTools,omitempty"`

	// RequireApproval lists tool-name patterns that need a human decision. It is
	// the server's own declaration, so a destructive tool stays gated whichever
	// agent is handed it.
	// +optional
	RequireApproval []string `json:"requireApproval,omitempty"`

	// ResourceMappings describes how to extract an authorization Resource from
	// a tool call's arguments.
	// +optional
	ResourceMappings *runtime.RawExtension `json:"resourceMappings,omitempty"`
}

// MCPServerStatus defines the observed state of MCPServer.
type MCPServerStatus struct {
	CommonStatus `json:",inline"`

	// Tools is what the last successful discovery found.
	// +optional
	Tools []MCPTool `json:"tools,omitempty"`

	// LastDiscoverTime is when discovery last succeeded.
	// +optional
	LastDiscoverTime *metav1.Time `json:"lastDiscoverTime,omitempty"`
}

// MCPTool describes one tool a server exposes.
type MCPTool struct {
	Name string `json:"name"`
	// +optional
	Description string `json:"description,omitempty"`
	// +optional
	SchemaHash string `json:"schemaHash,omitempty"`
}

// MCPServer condition types beyond the shared ones.
const (
	// ConditionHandshakeReady reports that the server completed the MCP
	// handshake.
	ConditionHandshakeReady = "HandshakeReady"
	// ConditionToolsDiscovered reports that the tool list was refreshed.
	ConditionToolsDiscovered = "ToolsDiscovered"
)

// MCPServerAnnotationAutoGrant is the per-object switch for the controller
// managed <name>-grant AuthorizationRule. The value "false" (case-insensitive,
// surrounding space ignored) turns the automatic rule off and removes one this
// controller created. Any other value, or no annotation, leaves it enabled.
const MCPServerAnnotationAutoGrant = "agent.onexai.io/auto-grant"

// MCPServerGrantRuleSuffix names the server-level allow rule for an MCPServer.
//
// The full name is <server>-grant, matching OneX's model.MCPGrantRuleNameFor
// byte for byte: OneX creates the same rule with GET-then-POST, so both sides
// converge on one object instead of writing two rules nobody can tell apart.
const MCPServerGrantRuleSuffix = "-grant"

const (
	// LabelGrantManagedBy marks an AuthorizationRule owned by a controller.
	LabelGrantManagedBy = "agent.onexai.io/managed-by"

	// ManagedByMCPServerController is the value LabelGrantManagedBy carries for
	// the rules this controller creates.
	ManagedByMCPServerController = "mcpserver-controller"

	// AnnotationGrantRef records the owning MCPServer as "namespace/name". It
	// is an annotation rather than a label because a server name is a DNS
	// subdomain; a label value is capped at 63 characters.
	AnnotationGrantRef = "agent.onexai.io/auto-grant-ref"

	// AnnotationGrantOwnerUID records the owning MCPServer UID for debugging.
	AnnotationGrantOwnerUID = "agent.onexai.io/auto-grant-owner-uid"
)

// MCPGrantRuleName returns the conventional authorization-rule name for an MCP
// server: <name>-grant.
func MCPGrantRuleName(serverName string) string {
	return serverName + MCPServerGrantRuleSuffix
}

// MCPServerOwnerReference builds the controller ownerReference an auto-grant
// carries, so the garbage collector removes the rule with its server.
func MCPServerOwnerReference(serverName string, serverUID types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         SchemeGroupVersion.String(),
		Kind:               "MCPServer",
		Name:               serverName,
		UID:                serverUID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MCPServerList is a list of MCPServer objects.
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MCPServer `json:"items"`
}
