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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CustomCommand is a user-defined slash command. Its parameters are declared
// per command rather than fixed, and running it is a REST invocation rather
// than a compiled-in action. Agents bind commands by Reference or LabelSelector.
type CustomCommand struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec CustomCommandSpec `json:"spec,omitempty"`
	// +optional
	Status CustomCommandStatus `json:"status,omitempty"`
}

// CustomCommandSpec defines the desired state of CustomCommand.
type CustomCommandSpec struct {
	CommonSpec `json:",inline"`

	// Type decides what an invocation executes: prompt, http or tool.
	Type string `json:"type"`

	// Aliases are extra names the command answers to.
	// +optional
	Aliases []string `json:"aliases,omitempty"`

	// Usage is the human-facing invocation shape, e.g. "<service> [--env=prod]".
	// +optional
	Usage string `json:"usage,omitempty"`

	// Parameters declares the command's arguments. The set is arbitrary per
	// command; the CLI-facing fields are what the chat service turns into flags.
	// +optional
	// +listType=atomic
	Parameters []CommandParameter `json:"parameters,omitempty"`

	// Prompt is set when Type is prompt.
	// +optional
	Prompt *PromptCommandSpec `json:"prompt,omitempty"`

	// HTTP is set when Type is http.
	// +optional
	HTTP *HTTPCommandSpec `json:"http,omitempty"`

	// Tool is set when Type is tool.
	// +optional
	Tool *ToolCommandSpec `json:"tool,omitempty"`

	// Output renders the result of an http or tool command.
	// +optional
	Output *CommandOutputSpec `json:"output,omitempty"`

	// TimeoutSeconds bounds one invocation. Zero means the deployment default.
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

// CommandParameter is one strictly-typed argument of a custom command.
type CommandParameter struct {
	// Name is the flag name and the template variable name.
	Name string `json:"name"`

	// In is "flag" (default) or "positional".
	// +optional
	In string `json:"in,omitempty"`

	// Type is string|integer|number|boolean|duration|array|object|json.
	// +optional
	Type string `json:"type,omitempty"`

	// Description becomes the flag's usage text.
	// +optional
	Description string `json:"description,omitempty"`

	// Required means the argument must be given explicitly. It is mutually
	// exclusive with Default.
	// +optional
	Required bool `json:"required,omitempty"`

	// Default is the value used when the argument is omitted.
	// +optional
	Default *runtime.RawExtension `json:"default,omitempty"`

	// Enum restricts a scalar argument to these values.
	// +optional
	Enum []string `json:"enum,omitempty"`

	// Format is an optional named format check (k8s-name, url, json, ...).
	// +optional
	Format string `json:"format,omitempty"`

	// Short is the single-character flag shorthand.
	// +optional
	Short string `json:"short,omitempty"`

	// Schema constrains object, array and json arguments.
	// +optional
	Schema *runtime.RawExtension `json:"schema,omitempty"`
}

// PromptCommandSpec renders a template and hands the result to the agent.
type PromptCommandSpec struct {
	// Template is a text/template rendered with .Params, .Command, .Agent,
	// .Conversation, .User, .DryRun and .Now.
	Template string `json:"template"`
}

// ServiceReference points at an in-cluster Service.
type ServiceReference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	// +optional
	Port int32 `json:"port,omitempty"`
	// +optional
	Path string `json:"path,omitempty"`
}

// HTTPHeader is one outbound header. Its value is a template.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// HTTPCommandSpec proxies an external REST endpoint. URL and Service are
// mutually exclusive; the shape follows admissionregistration's clientConfig.
type HTTPCommandSpec struct {
	// +optional
	Method string `json:"method,omitempty"`

	// URL is the absolute endpoint. It is mutually exclusive with Service.
	// +optional
	URL string `json:"url,omitempty"`

	// Service addresses an in-cluster Service. It is mutually exclusive with URL.
	// +optional
	Service *ServiceReference `json:"service,omitempty"`

	// CABundle is a PEM bundle used to verify the endpoint's certificate.
	// +optional
	CABundle string `json:"caBundle,omitempty"`

	// InsecureSkipTLSVerify disables certificate verification. It is strongly
	// discouraged and defaults to false.
	// +optional
	InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`

	// ClientConfig is the shared Kubernetes-shaped endpoint configuration. When
	// set it replaces URL, Service, CABundle and InsecureSkipTLSVerify.
	// +optional
	ClientConfig *ClientConfig `json:"clientConfig,omitempty"`

	// Headers are rendered and sent with the request.
	// +optional
	// +listType=atomic
	Headers []HTTPHeader `json:"headers,omitempty"`

	// Query is rendered and appended to the URL.
	// +optional
	Query map[string]string `json:"query,omitempty"`

	// Body is a rendered request body.
	// +optional
	Body string `json:"body,omitempty"`

	// +optional
	ContentType string `json:"contentType,omitempty"`

	// SecretRef names a Secret whose value is sent as a bearer token.
	// +optional
	SecretRef *Reference `json:"secretRef,omitempty"`

	// SideEffects is None|NoneOnDryRun|Some|Unknown. Only None and NoneOnDryRun
	// permit a dry-run invocation.
	// +optional
	SideEffects string `json:"sideEffects,omitempty"`
}

// ToolCommandSpec calls one tool on an MCP server.
type ToolCommandSpec struct {
	// MCPServer names the MCP server, which must also be bound to the agent.
	MCPServer Reference `json:"mcpServer"`

	// Tool is the MCP wire name of the tool.
	Tool string `json:"tool"`

	// ArgumentsTemplate renders a JSON object of tool arguments.
	// +optional
	ArgumentsTemplate string `json:"argumentsTemplate,omitempty"`
}

// CommandOutputSpec renders the result of an http or tool command.
type CommandOutputSpec struct {
	// Format is text, json or template.
	// +optional
	Format string `json:"format,omitempty"`

	// Template is rendered with .Status, .Header, .Body, .JSON and .Params.
	// +optional
	Template string `json:"template,omitempty"`

	// Schema constrains the result body.
	// +optional
	Schema *runtime.RawExtension `json:"schema,omitempty"`
}

// CustomCommandStatus defines the observed state of CustomCommand.
type CustomCommandStatus struct {
	CommonStatus `json:",inline"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CustomCommandList is a list of CustomCommand objects.
type CustomCommandList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []CustomCommand `json:"items"`
}
