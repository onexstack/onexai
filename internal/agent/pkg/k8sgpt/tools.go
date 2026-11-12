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

package k8sgpt

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/mark3labs/mcp-go/mcp"
)

// Routing argument names. They are added to every tool's schema and removed
// again before the call is forwarded, because no k8sgpt server knows about
// them — they exist only so the model can say which cluster it means.
const (
	argCluster = "cluster_name"
	argAccount = "account_id"
)

// BuildTools returns the fleet's tools, or nil when no cluster is connected.
//
// The tool list comes from one server because the fleet is homogeneous: taking
// the union would produce the same names several times over and leave the model
// choosing between duplicates. Each tool is then given the two routing
// arguments, with the connected clusters and accounts as their allowed values,
// which is what makes "which cluster" a question the model can answer.
func (r *Router) BuildTools(ctx context.Context) ([]tool.BaseTool, error) {
	baseCli, source := r.primaryClient()
	if baseCli == nil {
		return nil, nil
	}

	clusters := r.Clusters()
	accounts := r.Accounts()
	if len(clusters) == 0 {
		return nil, nil
	}

	resp, err := baseCli.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("k8sgpt: list tools from %s: %w", source, err)
	}

	tools := make([]tool.BaseTool, 0, len(resp.Tools))
	for _, original := range resp.Tools {
		spec, err := routingSchema(original, clusters, accounts)
		if err != nil {
			// A tool whose schema cannot be rewritten is dropped rather than
			// offered without the arguments that route it: routing on a guess
			// would run diagnostics against the wrong cluster.
			continue
		}
		tools = append(tools, &proxyTool{
			name:   original.Name,
			desc:   original.Description,
			schema: spec,
			route:  r,
			tool:   original.Name,
		})
	}
	return tools, nil
}

// routingSchema rewrites one MCP tool's input schema to include the two routing
// arguments, returning the JSON the proxy tool advertises.
func routingSchema(original mcp.Tool, clusters, accounts []string) ([]byte, error) {
	raw, err := json.Marshal(original.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("marshal input schema: %w", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("decode input schema: %w", err)
	}

	properties, ok := spec["properties"].(map[string]any)
	if !ok {
		properties = make(map[string]any)
		spec["properties"] = properties
	}
	properties[argCluster] = map[string]any{
		"type":        "string",
		"description": "要分析的 Kubernetes 集群名称",
		"enum":        clusters,
	}
	properties[argAccount] = map[string]any{
		"type":        "string",
		"description": "该集群所在的账号",
		"enum":        accounts,
	}

	required, _ := spec["required"].([]any)
	spec["required"] = append(required, argCluster, argAccount)

	return json.Marshal(spec)
}

// proxyTool is one routing-aware view of an MCP tool.
type proxyTool struct {
	name   string
	desc   string
	schema []byte
	route  *Router
	tool   string
}

// Info advertises the tool with its routing arguments.
func (p *proxyTool) Info(context.Context) (*schema.ToolInfo, error) {
	params, err := einoParams(p.schema)
	if err != nil {
		return nil, err
	}
	return &schema.ToolInfo{Name: p.name, Desc: p.desc, ParamsOneOf: schema.NewParamsOneOfByParams(params)}, nil
}

// InvokableRun strips the routing arguments and forwards the rest to the
// cluster they name.
func (p *proxyTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("k8sgpt %s: invalid arguments: %w", p.name, err)
	}

	cluster, err := stringArg(args, argCluster)
	if err != nil {
		return "", fmt.Errorf("k8sgpt %s: %w", p.name, err)
	}
	account, err := stringArg(args, argAccount)
	if err != nil {
		return "", fmt.Errorf("k8sgpt %s: %w", p.name, err)
	}

	// The routing arguments are this package's, not the server's.
	delete(args, argCluster)
	delete(args, argAccount)

	return p.route.CallTool(ctx, account, cluster, p.tool, args)
}

// stringArg reads a required string argument.
func stringArg(args map[string]any, name string) (string, error) {
	raw, ok := args[name]
	if !ok {
		return "", fmt.Errorf("missing %s argument", name)
	}
	value, ok := raw.(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%s must be a non-empty string", name)
	}
	return value, nil
}

// einoParams converts a JSON Schema object into eino's parameter map.
func einoParams(raw []byte) (map[string]*schema.ParameterInfo, error) {
	var spec struct {
		Properties map[string]struct {
			Type        string   `json:"type"`
			Description string   `json:"description"`
			Enum        []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("decode tool schema: %w", err)
	}

	required := make(map[string]struct{}, len(spec.Required))
	for _, name := range spec.Required {
		required[name] = struct{}{}
	}

	params := make(map[string]*schema.ParameterInfo, len(spec.Properties))
	for name, prop := range spec.Properties {
		_, isRequired := required[name]
		params[name] = &schema.ParameterInfo{
			Type:     schema.DataType(prop.Type),
			Desc:     prop.Description,
			Required: isRequired,
			Enum:     prop.Enum,
		}
	}
	return params, nil
}
