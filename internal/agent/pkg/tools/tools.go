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

// Package tools provides the built-in eino tools available to the agent. The
// registry is consumed by the eino/adk engine when assembling a
// ChatModelAgent's ToolsConfig.
//
// Only built-in tools live here. MCP-backed tools are resolved per run by
// pkg/mcpclient, which scopes them to the MCP servers the selected agents
// declare; callers compose the two sets. Keeping them apart is deliberate — a
// single registry with an "empty allowlist means everything" rule would expose
// every MCP tool to every run.
package tools

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/store"
	agent "github.com/onexstack/onexai/pkg/agentapi"
)

// Registry holds the built-in tools in registration order.
type Registry struct {
	byName map[string]tool.BaseTool
	order  []string
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{byName: make(map[string]tool.BaseTool)}
}

// Register adds a tool under name (ignoring nil tools / empty names).
func (r *Registry) Register(name string, t tool.BaseTool) {
	if name == "" || t == nil {
		return
	}
	if _, ok := r.byName[name]; !ok {
		r.order = append(r.order, name)
	}
	r.byName[name] = t
}

// Builtins returns every registered built-in tool, in registration order.
//
// There is no allowlist variant: it used to have one whose empty value meant
// "return everything", which is exactly the wrong default once tools can come
// from configured MCP servers. A run with no MCP servers selected must get
// these three tools and nothing else.
func (r *Registry) Builtins() []tool.BaseTool {
	out := make([]tool.BaseTool, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.byName[n])
	}
	return out
}

// Names returns the registered tool names in registration order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// AskUserQuestionTool is the built-in tool whose contract is to stop and ask a
// human. Unattended runs (a scheduled occurrence, say) have nobody to ask, so
// they drop it from their tool set rather than park on an answer that never
// comes.
const AskUserQuestionTool = "ask_user_question"

// noArgs is the input type for parameter-less tools.
type noArgs struct{}

// askUserQuestionArgs is the input schema for the ask_user_question tool.
type askUserQuestionArgs struct {
	Question    string   `json:"question" jsonschema:"description=The question to ask the user"`
	Options     []string `json:"options" jsonschema:"description=The selectable answer options"`
	MultiSelect bool     `json:"multiSelect,omitempty" jsonschema:"description=Whether the user may select multiple options"`
}

// askUserQuestion is the tool function backing ask_user_question. On the first
// invocation it interrupts to collect the answer; on resume (targeted with the
// selected answer via GetResumeContext) it returns that answer as its result.
func askUserQuestion(ctx context.Context, args askUserQuestionArgs) (string, error) {
	wasInterrupted, _, _ := tool.GetInterruptState[any](ctx)
	if !wasInterrupted {
		return "", tool.Interrupt(ctx, &agent.AskQuestionInfo{
			Question:    args.Question,
			Options:     args.Options,
			MultiSelect: args.MultiSelect,
		})
	}

	isTarget, hasData, data := tool.GetResumeContext[string](ctx)
	if !isTarget {
		// A sibling interrupt was resumed instead of this one; re-interrupt.
		return "", tool.Interrupt(ctx, nil)
	}
	if hasData {
		return data, nil
	}
	return "", errors.New("ask_user_question resumed without an answer")
}

// NewDefaultRegistry builds the built-in tool set. Resource-backed tools are
// added only when a resource store is configured.
func NewDefaultRegistry(rs store.AIStore) *Registry {
	r := New()

	if t, err := utils.InferTool(
		"current_time",
		"Return the current server time in RFC3339 format.",
		func(_ context.Context, _ noArgs) (string, error) {
			return time.Now().Format(time.RFC3339), nil
		},
	); err == nil {
		r.Register("current_time", t)
	}

	if t, err := utils.InferTool(
		AskUserQuestionTool,
		"Ask the user a question and wait for their answer. Use this when you need a clarification or decision from the user before proceeding.",
		askUserQuestion,
	); err == nil {
		r.Register(AskUserQuestionTool, t)
	}

	if rs != nil {
		if t, err := utils.InferTool(
			"list_agent",
			"List the names of configured AI agents.",
			func(ctx context.Context, _ noArgs) (string, error) {
				_, agents, err := rs.Agents().List(ctx, where.L(-1))
				if err != nil {
					return "", err
				}
				names := make([]string, 0, len(agents))
				for _, a := range agents {
					names = append(names, a.Name)
				}
				if len(names) == 0 {
					return "(no agents configured)", nil
				}
				return strings.Join(names, ", "), nil
			},
		); err == nil {
			r.Register("list_agent", t)
		}
	}

	return r
}
