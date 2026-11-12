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

package protocol

// ProtocolVersion is the MCP protocol revision implemented by oai-mcpserver.
const ProtocolVersion = "2024-11-05"

// MCP method names understood by the handler dispatcher.
const (
	MethodInitialize        = "initialize"
	MethodNotificationsInit = "notifications/initialized"
	MethodToolsList         = "tools/list"
	MethodToolsCall         = "tools/call"
	MethodNamespacesList    = "x-namespaces/list"
)

// Implementation identifies the client or server implementation in the
// initialize handshake, per the MCP specification.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolsCapability advertises tool-related server capabilities.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// ServerCapabilities advertises the capabilities oai-mcpserver supports.
type ServerCapabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// InitializeParams is the payload of the "initialize" request.
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ClientInfo      Implementation `json:"clientInfo"`
	Capabilities    map[string]any `json:"capabilities"`
}

// InitializeResult is the payload returned from the "initialize" request.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ServerInfo      Implementation     `json:"serverInfo"`
	Capabilities    ServerCapabilities `json:"capabilities"`
}

// Tool describes a single callable tool as exposed on the wire.
// Name follows the "<namespace>_<name>" convention (see internal/mcpserver/registry),
// analogous to how Kubernetes qualifies a resource as "<namespace>/<name>".
type Tool struct {
	Name        string         `json:"name"`
	Namespace   string         `json:"namespace"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ListToolsParams is the payload of the "tools/list" request.
// Namespace is an oai-mcpserver extension: when set, only tools registered
// under that namespace are returned.
type ListToolsParams struct {
	Namespace string `json:"namespace,omitempty"`
}

// ListToolsResult is the payload returned from the "tools/list" request.
type ListToolsResult struct {
	Tools []Tool `json:"tools"`
}

// CallToolParams is the payload of the "tools/call" request.
type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ContentBlock is a single unit of tool output content.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallToolResult is the payload returned from the "tools/call" request.
type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ListNamespacesResult is the payload returned from the "x-namespaces/list"
// extension method, listing every distinct namespace currently registered.
type ListNamespacesResult struct {
	Namespaces []string `json:"namespaces"`
}
