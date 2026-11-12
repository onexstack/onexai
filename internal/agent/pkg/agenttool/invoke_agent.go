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

// Package agenttool exposes delegation to the in-process runtime as a tool.
//
// A remote runtime reaches the same capability through the MCP gateway; this
// package is the in-process half of that contract, so an eino run and a remote
// run delegate with the same semantics and the same limits.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/capability"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// ToolName is the name the model sees. It matches the capability gateway's
// qualified name so a prompt written for one runtime works on the other.
const ToolName = "agent_invoke_agent"

// InvokeAgentTool runs another agent on this run's behalf.
type InvokeAgentTool struct {
	// Provider owns the authorization and the depth bound; this tool only
	// supplies the identity the run context carries.
	Provider *capability.AgentProvider
}

var _ tool.InvokableTool = (*InvokeAgentTool)(nil)

// New builds the tool, or nil when delegation is not configured.
func New(provider *capability.AgentProvider) tool.BaseTool {
	if provider == nil || provider.Invoker == nil {
		return nil
	}
	return &InvokeAgentTool{Provider: provider}
}

// Info implements tool.BaseTool.
func (t *InvokeAgentTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: ToolName,
		Desc: "把一件相对独立的工作交给另一个 Agent 处理，并拿回它的回答。对方有自己的指令、工具和会话，看不到当前对话，" +
			"所以调用时要把必要背景一并写进 message。对方如果需要人工确认，会返回 input-required 和待确认项；" +
			"把 taskId 连同 answer 再调一次即可继续。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"agent":   {Type: schema.String, Desc: "要调用的 Agent 名称。", Required: true},
			"message": {Type: schema.String, Desc: "交给对方的完整任务描述。", Required: true},
			"session": {Type: schema.String, Desc: "继续之前与该 Agent 的同一段对话时传入它返回的 session。"},
			"taskId":  {Type: schema.String, Desc: "回答对方暂停中的调用时传入它返回的 taskId。"},
			"answer": {
				Type:     schema.Object,
				Desc:     "对暂停中调用的回答，需与 taskId 一起使用。",
				Required: false,
				SubParams: map[string]*schema.ParameterInfo{
					"requestId": {Type: schema.String, Desc: "暂停项的 requestId。", Required: true},
					"decision":  {Type: schema.String, Desc: "allow、deny 或 allow_all。", Required: true, Enum: []string{"allow", "deny", "allow_all"}},
					"allowAll":  {Type: schema.Boolean, Desc: "后续同类请求不再询问。"},
				},
			},
		}),
	}, nil
}

// InvokableRun implements tool.InvokableTool.
func (t *InvokeAgentTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	delegation, ok := agentapi.DelegationFromContext(ctx)
	if !ok || len(delegation.Agents) == 0 {
		return "", fmt.Errorf("%s: this run has no agent it may call", ToolName)
	}
	var args capability.AgentArguments
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("%s: decode arguments: %w", ToolName, err)
	}
	if !t.Provider.Allows(delegation.Agents, args.Agent) {
		return "", fmt.Errorf("%s: agent %q is not allowed to call %q", ToolName, delegation.Agents[0], args.Agent)
	}
	result, err := t.Provider.Call(ctx, capability.AgentRequest{
		Arguments:     args,
		ParentSession: delegation.ParentSession,
		RunID:         agentapi.RunIDFromContext(ctx),
		RootRunID:     delegation.RootRunID,
		Depth:         delegation.Depth,
		Subject:       delegation.Subject,
		SubjectKind:   delegation.SubjectKind,
	})
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("%s: encode result: %w", ToolName, err)
	}
	return string(encoded), nil
}
