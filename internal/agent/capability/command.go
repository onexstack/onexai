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

package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/onexstack/onexstack/pkg/store/where"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/onexstack/onexai/internal/agent/store"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// CommandInvoker is the custom-command service as the gateway needs it.
type CommandInvoker interface {
	Invoke(ctx context.Context, name string, req *v1.InvokeCustomCommandRequest) (*v1.InvokeCustomCommandResponse, error)
}

// CommandProvider exposes an agent's bound custom commands as MCP tools.
type CommandProvider struct {
	Resources store.AIStore
	Commands  CommandInvoker
}

// Namespace implements Provider.
func (p *CommandProvider) Namespace() string { return "command" }

// Tools implements Provider.
func (p *CommandProvider) Tools() []Tool {
	return []Tool{{
		Namespace:   "command",
		Name:        "invoke_custom_command",
		Description: "Run a custom command this agent is bound to. The command's declared parameters are validated by the service.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name":   map[string]any{"type": "string", "description": "The command name"},
				"params": map[string]any{"type": "object", "description": "Command parameters"},
			},
		},
		Handler: p.invoke,
	}}
}

func (p *CommandProvider) invoke(ctx context.Context, claims Claims, args json.RawMessage) (any, error) {
	var input struct {
		Name   string         `json:"name"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(args, &input); err != nil || input.Name == "" {
		return nil, fmt.Errorf("invoke_custom_command needs a command name")
	}
	agent, err := p.Resources.Agents().Get(ctx, where.F("name", claims.Agent))
	if err != nil {
		return nil, fmt.Errorf("resolve agent %q: %w", claims.Agent, err)
	}
	allowed := agent.EffectiveCustomCommands()
	if claims.Narrowed {
		allowed = claims.CustomCommands
	}
	if !slices.Contains(allowed, input.Name) {
		return nil, fmt.Errorf("this run does not bind custom command %q", input.Name)
	}
	params, err := structpb.NewStruct(input.Params)
	if err != nil {
		return nil, fmt.Errorf("encode command parameters: %w", err)
	}
	resp, err := p.Commands.Invoke(ctx, input.Name, &v1.InvokeCustomCommandRequest{
		Uid:       claims.RunID,
		Agent:     claims.Agent,
		Namespace: agent.Namespace,
		User:      &v1.UserInfo{Uid: claims.Subject, Username: claims.Subject},
		Context:   &v1.InvocationContext{ConversationId: claims.SessionKey, Surface: "mcp"},
		Params:    params,
	})
	if err != nil {
		return nil, err
	}
	status := resp.GetStatus()
	return map[string]any{
		"phase":   status.GetPhase(),
		"reason":  status.GetReason(),
		"message": status.GetMessage(),
		"usage":   status.GetUsage(),
	}, nil
}
