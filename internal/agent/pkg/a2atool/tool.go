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

// Package a2atool exposes registered external A2A agents to an in-process run.
//
// One dispatcher tool carries the allowed targets as an enum instead of one
// tool per remote agent: the model chooses a target by name, and adding or
// removing a call does not change the shape of the tool set the runner cache
// fingerprints.
package a2atool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/a2ainvoke"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authz"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// ToolName is the model-facing dispatch tool.
const ToolName = "a2a_invoke"

// Provider builds the dispatcher from the agents a run may call.
type Provider struct {
	Agents       store.AgentStore
	Invoke       *a2ainvoke.Service
	MaxOutputLen int
}

// New builds a provider, or nil when the service is not configured.
func New(agents store.AgentStore, invoke *a2ainvoke.Service) *Provider {
	if agents == nil || invoke == nil {
		return nil
	}
	return &Provider{Agents: agents, Invoke: invoke, MaxOutputLen: 32 << 10}
}

// Tools returns the dispatcher for the named calls, or nil when none of them
// is an external A2A agent.
func (p *Provider) Tools(ctx context.Context, names []string) []tool.BaseTool {
	if p == nil || p.Agents == nil || len(names) == 0 {
		return nil
	}
	targets := make([]*model.AgentM, 0, len(names))
	for _, name := range names {
		row, err := p.Agents.Get(ctx, where.F("name", name))
		if err != nil || row == nil || !isExternal(row) {
			continue
		}
		targets = append(targets, row)
	}
	if len(targets) == 0 {
		return nil
	}
	return []tool.BaseTool{&invokeTool{provider: p, targets: targets}}
}

type invokeTool struct {
	provider *Provider
	targets  []*model.AgentM
}

var _ tool.InvokableTool = (*invokeTool)(nil)

func (t *invokeTool) Info(context.Context) (*schema.ToolInfo, error) {
	names := make([]string, 0, len(t.targets))
	var descriptions strings.Builder
	for _, target := range t.targets {
		names = append(names, target.Name)
		descriptions.WriteString("\n- ")
		descriptions.WriteString(target.Name)
		if target.DisplayName != "" {
			descriptions.WriteString(" (" + target.DisplayName + ")")
		}
		if target.Description != "" {
			descriptions.WriteString(": " + target.Description)
		}
		if skills := skillNames(target); len(skills) > 0 {
			descriptions.WriteString("；技能：" + strings.Join(skills, "、"))
		}
	}
	return &schema.ToolInfo{
		Name: ToolName,
		Desc: "调用一个已注册的外部 A2A 智能体并拿回它的回答。可用的目标：" + descriptions.String() +
			"\n如果对方返回 input-required，把 taskId 和用户的补充信息再调一次即可继续。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"agent": {
				Type: schema.String, Desc: "目标外部 A2A 智能体名称。", Required: true, Enum: names,
			},
			"message": {Type: schema.String, Desc: "交给对方的完整任务描述。", Required: true},
			"context_id": {Type: schema.String, Desc: "继续同一段对话时传入对方返回的 contextId。"},
			"task_id":    {Type: schema.String, Desc: "回答对方暂停中的调用时传入对方返回的 taskId。"},
		}),
	}, nil
}

type arguments struct {
	Agent     string `json:"agent"`
	Message   string `json:"message"`
	ContextID string `json:"context_id"`
	TaskID    string `json:"task_id"`
}

func (t *invokeTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	var args arguments
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", fmt.Errorf("%s: decode arguments: %w", ToolName, err)
	}
	if args.Agent == "" || args.Message == "" {
		return "", fmt.Errorf("%s: agent and message are required", ToolName)
	}
	var target *model.AgentM
	for _, candidate := range t.targets {
		if candidate.Name == args.Agent {
			target = candidate
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("%s: agent %q is not one of this run's calls", ToolName, args.Agent)
	}

	call := a2ainvoke.CallRequest{
		Name:      target.Name,
		Message:   args.Message,
		ContextID: args.ContextID,
		TaskID:    args.TaskID,
		Source:    "agent-tool",
	}
	if subject, ok := authz.SubjectFromContext(ctx); ok {
		call.CallerKind = string(subject.Kind)
		call.CallerID = subject.PrimaryID()
		call.CallerName = subject.UserName
	}
	call.ParentRunID = agentapi.RunIDFromContext(ctx)
	if call.ParentRunID != "" {
		call.CallerKind = "agent"
	}

	result, err := t.provider.Invoke.Invoke(ctx, call)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"state":     "",
		"taskId":    "",
		"contextId": "",
		"text":      "",
	}
	if result.Result != nil {
		payload["state"] = result.Result.State
		payload["taskId"] = result.Result.TaskID
		payload["contextId"] = result.Result.ContextID
		payload["text"] = truncate(result.Result.Text, t.maxOutput())
		payload["artifacts"] = result.Result.Artifacts
		if result.Result.State == "input-required" || result.Result.State == "auth-required" {
			payload["interrupt"] = map[string]any{
				"kind":    result.Result.State,
				"message": result.Result.Message,
			}
		}
		if result.Result.State == "failed" || result.Result.State == "rejected" {
			payload["error"] = result.Result.Message
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (t *invokeTool) maxOutput() int {
	if t.provider.MaxOutputLen > 0 {
		return t.provider.MaxOutputLen
	}
	return 32 << 10
}

func isExternal(row *model.AgentM) bool {
	if row.Kind != "external" {
		return false
	}
	var execution model.AgentExecutionM
	if row.ExecutionJSON == "" || json.Unmarshal([]byte(row.ExecutionJSON), &execution) != nil {
		return false
	}
	return execution.Mode == "a2a" && execution.A2A != nil
}

func skillNames(row *model.AgentM) []string {
	var status model.AgentA2AStatusM
	if row.A2AStatusJSON == "" || json.Unmarshal([]byte(row.A2AStatusJSON), &status) != nil {
		return nil
	}
	out := make([]string, 0, len(status.Skills))
	for _, skill := range status.Skills {
		if skill.Name != "" {
			out = append(out, skill.Name)
		}
	}
	return out
}

func truncate(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
