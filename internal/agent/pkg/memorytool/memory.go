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

// Package memorytool exposes long-term memory to the in-process runtime.
//
// A remote runtime reaches the same three operations through the MCP capability
// gateway; these are the eino half of that contract, calling the same provider
// so the two transports cannot drift apart. Without them an in-process agent
// could only rely on the gateway's automatic save and prefetch — its model had
// no way to remember something deliberately.
package memorytool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/capability"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// Tool names the model sees. They match the capability gateway's so a prompt
// written for one runtime works on the other.
const (
	SaveToolName     = "save_memory"
	LoadToolName     = "load_memory"
	PrefetchToolName = "prefetch_memory"
)

// New builds the three memory tools, or nil when memory is not configured.
func New(provider *capability.MemoryProvider) []tool.BaseTool {
	if provider == nil || provider.Service == nil {
		return nil
	}
	return []tool.BaseTool{
		&memoryTool{provider: provider, name: SaveToolName, info: saveInfo()},
		&memoryTool{provider: provider, name: LoadToolName, info: loadInfo()},
		&memoryTool{provider: provider, name: PrefetchToolName, info: prefetchInfo()},
	}
}

// memoryTool is one of the three, differing only in its name and schema.
type memoryTool struct {
	provider *capability.MemoryProvider
	name     string
	info     *schema.ToolInfo
}

var _ tool.InvokableTool = (*memoryTool)(nil)

// Info implements tool.BaseTool.
func (t *memoryTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }

// InvokableRun implements tool.InvokableTool.
func (t *memoryTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	identity, ok := agentapi.MemoryContextFrom(ctx)
	if !ok || identity.Agent == "" || identity.Subject == "" {
		return "", fmt.Errorf("%s: this run has no memory identity", t.name)
	}
	result, err := t.provider.Invoke(ctx, capability.Claims{
		RunID:   agentapi.RunIDFromContext(ctx),
		Agent:   identity.Agent,
		Subject: identity.Subject,
	}, t.name, json.RawMessage(arguments))
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("%s: encode result: %w", t.name, err)
	}
	return string(encoded), nil
}

func saveInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: SaveToolName,
		Desc: "把一条值得长期记住的事实、偏好或结论写入长期记忆，供以后的会话检索。" +
			"只存一句可独立理解的内容，不要存整段对话。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"content": {Type: schema.String, Desc: "要记住的内容。", Required: true},
		}),
	}
}

func loadInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: LoadToolName,
		Desc: "检索当前用户的长期记忆，返回与问题最相关的若干条。用于回答“我们之前是不是说过……”这类问题。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: schema.String, Desc: "要检索的问题或关键词。", Required: true},
			"limit": {Type: schema.Integer, Desc: "最多返回多少条。"},
		}),
	}
}

func prefetchInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: PrefetchToolName,
		Desc: "返回一段紧凑的记忆摘要，适合在开始处理一个话题前一次性了解背景。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: schema.String, Desc: "话题或问题。", Required: true},
		}),
	}
}
