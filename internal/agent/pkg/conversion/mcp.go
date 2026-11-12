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

package conversion

import (
	"encoding/json"

	"github.com/onexstack/onexai/internal/agent/model"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// MCPV1ToMCPModel converts a proto MCP server to the domain model.
func MCPV1ToMCPModel(p *v1.MCP) *model.MCPM {
	if p == nil {
		return &model.MCPM{}
	}
	out := &model.MCPM{
		Name:             p.GetName(),
		DisplayName:      p.GetDisplayName(),
		Description:      p.GetDescription(),
		Status:           p.GetStatus(),
		Mode:             p.GetMode(),
		Protocol:         p.GetProtocol(),
		URL:              p.GetUrl(),
		Headers:          p.GetHeaders(),
		AllowedTools:     p.GetAllowedTools(),
		RequireApproval:  p.GetRequireApprovals(),
		ResourceMappings: p.GetResourceMappings(),
		// Tools is deliberately not copied: it is observed status written by
		// the runtime health reporter, not client input. Trusting the request
		// would let a caller fake a discovery result.
		Namespace: p.GetNamespace(),
		Labels:    p.GetLabels(),
		CreatedAt: p.GetCreatedAt(),
		UpdatedAt: p.GetUpdatedAt(),
	}
	if cc := p.GetClientConfig(); cc != nil {
		if data, err := json.Marshal(ClientConfigV1ToCR(cc)); err == nil {
			out.ClientConfigJSON = string(data)
		}
		if cc.GetUrl() != "" {
			out.URL = cc.GetUrl()
		}
	}
	return out
}

// MCPModelToMCPV1 converts the domain model back to the wire shape.
func MCPModelToMCPV1(m *model.MCPM) *v1.MCP {
	if m == nil {
		return nil
	}
	out := &v1.MCP{
		Name:             m.Name,
		DisplayName:      m.DisplayName,
		Description:      m.Description,
		Status:           StatusToV1(m.Status),
		Mode:             m.Mode,
		Protocol:         m.Protocol,
		Url:              m.URL,
		Headers:          m.Headers,
		AllowedTools:     m.AllowedTools,
		RequireApprovals: []string(m.RequireApproval),
		ResourceMappings: m.ResourceMappings,
		Tools:            mcpToolsToV1(m.Tools),
		Namespace:        m.Namespace,
		Labels:           m.Labels,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}
	if m.ClientConfigJSON != "" {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal([]byte(m.ClientConfigJSON), cfg); err == nil {
			out.ClientConfig = ClientConfigFromCR(cfg)
		}
	}
	return out
}

// mcpToolsToV1 renders the last discovered tool list for the wire. The order is
// preserved so the console can show tools in the order the server publishes
// them.
func mcpToolsToV1(tools []model.MCPTool) []*v1.MCPProbedTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]*v1.MCPProbedTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, &v1.MCPProbedTool{Name: tool.Name, Description: tool.Description})
	}
	return out
}
