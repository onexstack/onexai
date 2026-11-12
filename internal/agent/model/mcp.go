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

package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&MCPM{}) }

// MCPModeDirect is the only mode the platform supports: the runtime dials the
// server itself. The column predates the control-plane object, which has no
// mode field at all, so the store reports this value rather than an empty one.
const MCPModeDirect = "direct"

// MCPM describes an MCP (Model Context Protocol) server that exposes tools.
type MCPM struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string `gorm:"uniqueIndex;size:128" json:"name"`
	DisplayName string `gorm:"size:256" json:"display_name"`
	Description string `gorm:"type:text" json:"description"`
	Status      string `gorm:"size:32" json:"status"`
	Mode        string `gorm:"size:32" json:"mode"`     // e.g. "direct"
	Protocol    string `gorm:"size:32" json:"protocol"` // "SSE" | "HTTP"
	URL         string `gorm:"size:512" json:"url"`
	// ClientConfigJSON is the shared Kubernetes-shaped endpoint configuration,
	// serialized. Empty means the row predates ClientConfig and uses URL alone.
	ClientConfigJSON string `gorm:"column:client_config;type:text" json:"client_config"`
	// Headers is a JSON object string of custom HTTP headers, decoded by its
	// reader (see the MCP client). It is stored as written rather than through
	// StringList because it is an object, not a list.
	Headers      string     `gorm:"type:text" json:"headers"`
	AllowedTools StringList `gorm:"type:text" json:"allowed_tools"` // tool whitelist; empty = unrestricted
	// RequireApproval lists the tool-name patterns of this server that must be
	// approved by a human before they run: an exact name, a "delete_*" prefix
	// glob, or "*" for every tool the server exposes. It is the server's own
	// declaration of what is dangerous, which is why it is here rather than in
	// each agent that uses the server — a destructive MCP tool stays gated no
	// matter which agent is handed it. Empty means the server's tools run
	// unapproved.
	RequireApproval StringList `gorm:"type:text" json:"require_approval"`
	// ResourceMappings is a JSON array string of authz.ToolResourceMapping
	// entries describing how to extract a Resource from one of this server's
	// tool calls' arguments (e.g. a namespace, a service name) so resource-level
	// authorization has something to address. Empty means this server's tools
	// carry no resource mapping, and resource-level authorization is skipped
	// for them.
	//
	// Like Headers it is decoded by its reader rather than by the column,
	// because its element type lives in this package's consumer, not in it.
	ResourceMappings string `gorm:"type:text" json:"resource_mappings"`
	// Tools is the last successful tool discovery for this server. It is
	// observed state: the runtime health reporter writes it into the control
	// plane object's status, so there is no column to persist it in.
	Tools     []MCPTool `gorm:"-" json:"tools,omitempty"`
	Namespace string    `gorm:"column:namespace;size:128;index" json:"namespace"`
	Labels    StringMap `gorm:"-" json:"labels"`
	CreatedAt int64     `json:"created_at"`
	UpdatedAt int64     `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// MCPTool is one tool a server published, as recorded by the last successful
// discovery. It carries what the console needs to explain the tool: its name
// and its human-facing description.
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// TableName returns the MCP table name.
func (MCPM) TableName() string { return dbschema.Table(dbschema.Agent, "mcp") }

// Enabled reports whether the MCP server may be dialed.
func (m MCPM) Enabled() bool { return m.Status != StatusDisabled }

// GetNamespace returns the normalized namespace.
func (m MCPM) GetNamespace() string { return NormalizeNamespace(m.Namespace) }
