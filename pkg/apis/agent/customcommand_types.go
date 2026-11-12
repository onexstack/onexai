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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CustomCommand is the internal (hub) schema for the custom commands API.
type CustomCommand struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   CustomCommandSpec
	Status CustomCommandStatus
}

// CustomCommandSpec defines the desired state of CustomCommand.
type CustomCommandSpec struct {
	CommonSpec

	Type           string
	Aliases        []string
	Usage          string
	Parameters     []CommandParameter
	Prompt         *PromptCommandSpec
	HTTP           *HTTPCommandSpec
	Tool           *ToolCommandSpec
	Output         *CommandOutputSpec
	TimeoutSeconds int32
}

// CommandParameter is one strictly-typed argument of a custom command.
type CommandParameter struct {
	Name        string
	In          string
	Type        string
	Description string
	Required    bool
	Default     *runtime.RawExtension
	Enum        []string
	Format      string
	Short       string
	Schema      *runtime.RawExtension
}

// PromptCommandSpec renders a template and hands the result to the agent.
type PromptCommandSpec struct {
	Template string
}

// ServiceReference points at an in-cluster Service.
type ServiceReference struct {
	Namespace string
	Name      string
	Port      int32
	Path      string
}

// HTTPHeader is one outbound header. Its value is a template.
type HTTPHeader struct {
	Name  string
	Value string
}

// HTTPCommandSpec proxies an external REST endpoint.
type HTTPCommandSpec struct {
	Method string
	// URL, Service, CABundle and InsecureSkipTLSVerify are the legacy transport
	// fields. ClientConfig replaces them when set.
	URL                   string
	Service               *ServiceReference
	CABundle              string
	InsecureSkipTLSVerify bool
	ClientConfig          *ClientConfig
	Headers               []HTTPHeader
	Query                 map[string]string
	Body                  string
	ContentType           string
	SecretRef             *Reference
	SideEffects           string
}

// ToolCommandSpec calls one tool on an MCP server.
type ToolCommandSpec struct {
	MCPServer         Reference
	Tool              string
	ArgumentsTemplate string
}

// CommandOutputSpec renders the result of an http or tool command.
type CommandOutputSpec struct {
	Format   string
	Template string
	Schema   *runtime.RawExtension
}

// CustomCommandStatus defines the observed state of CustomCommand.
type CustomCommandStatus struct {
	CommonStatus
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CustomCommandList is a list of CustomCommand objects.
type CustomCommandList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []CustomCommand
}
