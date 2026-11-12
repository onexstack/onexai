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

package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// ToolResourceMapping describes how to extract a Resource from one Tool
// call's arguments (a flat JSON object). Field paths use simple field names
// (NOT full JSONPath) — sufficient for the flat/shallow argument shapes MCP
// tools use today. This can be upgraded to JSONPath later without changing
// the config storage format (MCPM.ResourceMappings) if nested extraction is
// ever needed.
type ToolResourceMapping struct {
	ToolName       string            `json:"tool_name"`
	ResourceKind   ResourceKind      `json:"resource_kind"`
	NamePath       string            `json:"name_path"`                 // e.g. "namespace" or "service_name"
	AttributePaths map[string]string `json:"attribute_paths,omitempty"` // extra fields, e.g. {"env":"env"}
	// Verb defaults to VerbRead; only mutating tools (e.g. "delete-resource")
	// need to declare "write" explicitly.
	Verb Verb `json:"verb,omitempty"`
}

// ParseResourceMappings parses the JSON array stored in MCPM.ResourceMappings.
// An empty string is not an error — it means "this MCP has no resource
// mappings configured", matching the field's documented default behavior.
func ParseResourceMappings(raw string) ([]ToolResourceMapping, error) {
	if raw == "" {
		return nil, nil
	}
	var mappings []ToolResourceMapping
	if err := json.Unmarshal([]byte(raw), &mappings); err != nil {
		return nil, fmt.Errorf("authz: parse resource_mappings: %w", err)
	}
	return mappings, nil
}

// mcpResourceExtractor implements ResourceExtractor by looking up
// ToolResourceMapping entries indexed by (mcpName, toolName). The index is
// built once per Runner assembly from the resolved MCPM.ResourceMappings of
// every MCP Server in play — not refetched per Tool call.
type mcpResourceExtractor struct {
	// mappings is keyed by mcpName -> toolName -> mapping.
	mappings map[string]map[string]ToolResourceMapping
}

// NewMCPResourceExtractor builds a ResourceExtractor from a pre-resolved
// (mcpName -> toolName -> mapping) index.
func NewMCPResourceExtractor(mappings map[string]map[string]ToolResourceMapping) ResourceExtractor {
	return &mcpResourceExtractor{mappings: mappings}
}

func (e *mcpResourceExtractor) Extract(ctx context.Context, mcpName, toolName, argumentsInJSON string) (Resource, Verb, error) {
	byTool, ok := e.mappings[mcpName]
	if !ok {
		return Resource{Kind: ResourceKindNone}, "", nil
	}
	mapping, ok := byTool[toolName]
	if !ok {
		return Resource{Kind: ResourceKindNone}, "", nil
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return Resource{Kind: ResourceKindNone}, "", fmt.Errorf("authz: unmarshal tool call arguments: %w", err)
	}

	name, _ := args[mapping.NamePath].(string)
	if name == "" {
		if mapping.NamePath != "" {
			slog.WarnContext(ctx, "authz: resource mapping NamePath not found in tool arguments",
				"mcp", mcpName, "tool", toolName, "name_path", mapping.NamePath)
		}
		return Resource{Kind: ResourceKindNone}, "", nil
	}

	var attrs map[string]string
	if len(mapping.AttributePaths) > 0 {
		attrs = make(map[string]string, len(mapping.AttributePaths))
		for attrKey, path := range mapping.AttributePaths {
			if v, ok := args[path].(string); ok {
				attrs[attrKey] = v
			}
		}
	}

	verb := mapping.Verb
	if verb == "" {
		verb = VerbRead
	}
	// A mapping addresses a tool call's arguments, so the only namespace it can
	// mean is the cluster one. Configurations written while that kind was still
	// spelled "namespace" (before the AI-resource kind took that name) keep
	// working by being read as k8s-namespace.
	kind := mapping.ResourceKind
	if kind == ResourceKindNamespace {
		kind = ResourceKindK8sNamespace
	}
	return Resource{Kind: kind, Name: name, Attributes: attrs}, verb, nil
}

// BuildResourceMappingIndex parses every mcpName's MCPM.ResourceMappings raw
// string into the mcpName -> toolName -> mapping index consumed by
// NewMCPResourceExtractor. Malformed JSON for one MCP is reported in the
// returned error map and simply excluded — it never blocks Runner assembly.
func BuildResourceMappingIndex(mcpResourceMappingsRaw map[string]string) (map[string]map[string]ToolResourceMapping, map[string]error) {
	index := make(map[string]map[string]ToolResourceMapping, len(mcpResourceMappingsRaw))
	errs := make(map[string]error)

	for mcpName, raw := range mcpResourceMappingsRaw {
		mappings, err := ParseResourceMappings(raw)
		if err != nil {
			errs[mcpName] = err
			continue
		}
		if len(mappings) == 0 {
			continue
		}
		byTool := make(map[string]ToolResourceMapping, len(mappings))
		for _, m := range mappings {
			if m.ToolName == "" {
				continue
			}
			byTool[m.ToolName] = m
		}
		if len(byTool) > 0 {
			index[mcpName] = byTool
		}
	}

	return index, errs
}
