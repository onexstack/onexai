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

package ai

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	askaiLong = templates.LongDesc(`
        该工具用于【向高级运维 AI 专家发起提问，获取排障建议、原理分析或操作指导】。

        【触发场景】：
        当用户遇到报错、询问技术原理、或者寻求排错思路时（如："Pod为什么一直Pending"、"解释一下 BGP 协议"、"帮我分析一下这段 Nginx 报错"），请调用此工具将问题透传给 AI 知识库。

        【参数与行为】：
        - [description] (位置参数)：必需。需要向 AI 提问的具体问题内容。
        - --reasoning, -r (标志参数)：开启"推理模式"。当遇到极为复杂的疑难杂症，或用户明确要求"深度分析"、"详细解释步骤"时，请附加此参数。开启后，模型会额外输出其底层的逻辑拆解与思考过程。`)

	askaiExample = templates.Examples(`
        # 场景1：普通技术提问（快速获取结论）
        /askai 请问K8s中Deployment和StatefulSet的区别是什么？

        # 场景2：复杂故障排查（开启深度推理模式）
        /askai --reasoning 线上数据库出现大量慢查询，且CPU飙升到100%，请给出详细的排查链路。`)
)

// AskAIOptions defines configuration for the "askai" command.
type AskAIOptions struct {
	out runtime.Responder

	Reasoning bool     // Whether to show the reasoning process
	Modules   []string // Duty module IDs to load, merging in their skill/mcp/kb
	Agents    []string // Names of the agents to load
	Debug     bool     // Whether to enable debug mode
	Stdout    bool     // Whether to write the final result to stdout (for automated tests)
	History   int      // Number of context messages to keep. -1 means the default behavior
}

// NewAskAICmd creates the "askai" command.
func NewAskAICmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &AskAIOptions{out: out, History: -1}

	cmd := &cobra.Command{
		Use:                   "askai <description>",
		Short:                 "向 AI 运维专家提问（支持通过 -r 参数开启深度推理模式）",
		Long:                  askaiLong,
		Example:               askaiExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "【必填参数】向 AI 提问的具体问题描述。支持拼接多个长句。",
			chat.ChatGroup:                   chat.ChatGroupAI,
		},

		RunE: func(cmd *cobra.Command, args []string) error {
			dr := runtime.From(cmd.Context())
			if err := o.Complete(cmd.Context(), dr); err != nil {
				return err
			}
			if err := o.Validate(cmd.Context(), args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), sr, dr, strings.Join(args, " "))
		},
	}

	cmd.Flags().BoolVarP(&o.Reasoning, "reasoning", "r", false, "是否开启深度推理模式，展示 LLM 的思维链（Thinking Process）")
	cmd.Flags().BoolVarP(&o.Debug, "debug", "d", false, "是否开启调试模式，在 Lark 卡片中展示详细的工具调用日志")
	cmd.Flags().StringSliceVar(&o.Modules, "modules", nil,
		"指定值班模块 ID 列表（逗号分隔，如 bke,eks），合并加载其配置的 skill、MCP 和知识库后进行推理")
	cmd.Flags().StringSliceVarP(&o.Agents, "agents", "a", nil, "指定要加载的 agent 名字列表（逗号分隔）")
	return cmd
}

// Complete sets default values.
func (o *AskAIOptions) Complete(ctx context.Context, dr *runtime.DynamicRuntime) error {
	// When -a is not given, use the agent resolved for the asker at this moment.
	//
	// It asks the scheduler rather than reading the conversation's row: the row
	// states a declaration, and what that declaration picks depends on who is
	// speaking. It used to read a recorded result and split it on "." — the
	// encoding of a column that has been a JSON array for some time, and that no
	// longer exists at all.
	//
	// The value is carried for the MCP schema and opsassist parity; see Run, which
	// does not forward it yet.
	if len(o.Agents) == 0 && dr != nil && dr.ResolvedAgents != nil {
		if res, err := dr.ResolvedAgents(ctx, dr); err == nil {
			o.Agents = res.Agents
		}
	}
	if o.Debug {
		o.Reasoning = true
	}
	return nil
}

// Validate ensures the question is present.
func (o *AskAIOptions) Validate(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return runtime.NewUserError("usage: /askai <question>")
	}
	return nil
}

// Run streams the question through oai-agent-apiserver and writes each delta to out.
//
// Note: --reasoning/--debug/--modules/--agents are accepted for MCP schema and
// opsassist parity, but the oai-agent-apiserver completion API does not yet consume
// them; the question is forwarded verbatim. The agent itself is not one of them:
// the API requires one, and this command pins v1.NoAgent so the run stays a
// one-shot completion.
func (o *AskAIOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, question string) error {
	return askViaAgent(ctx, sr, o.out, question)
}

// askViaAgent streams the question through oai-agent-apiserver and writes each
// delta chunk to out.
func askViaAgent(ctx context.Context, sr *runtime.StaticRuntime, out io.Writer, question string) error {
	dr := runtime.From(ctx)
	// A conversation with no id of its own falls back to the conversation the
	// reply would go to, which is the same key the message path uses. It is
	// deliberately not the caller's identity: an identity is not a session.
	sessionID := dr.ConvID
	if sessionID == "" {
		sessionID = dr.Address()
	}

	// Record AI request usage and duration.
	chatType := chat.ChatTypeP2P
	if dr.IsGroup() {
		chatType = chat.ChatTypeGroup
	}
	aiStart := time.Now()
	metrics.M.RecordAIRequest(ctx, chatType, dr.Identity())
	defer func() {
		metrics.M.RecordAIRequestDuration(ctx, time.Since(aiStart).Seconds())
	}()

	req := &v1.ChatCompletionRequest{
		SessionID: sessionID,
		// The far side requires an agent; NoAgent is its explicit request for the
		// builtin assistant — a one-shot completion with no tools, no knowledge
		// bases and no persona, which is what this command is for.
		Agent:    v1.NoAgent,
		Messages: []*v1.ChatCompletionMessage{{Role: "user", Content: question}},
	}
	err := sr.Clientset.Agent().ChatCompletionsStream(ctx, req, func(chunk *v1.ChatCompletionStreamResponse) error {
		for _, choice := range chunk.GetChoices() {
			if d := choice.GetDelta(); d != nil && d.GetContent() != "" {
				fmt.Fprint(out, d.GetContent())
			}
		}
		return nil
	})
	if err != nil {
		metrics.M.RecordAIRequestError(ctx, "stream_error")
	}
	return err
}
