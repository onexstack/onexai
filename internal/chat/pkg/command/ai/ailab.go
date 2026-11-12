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
	"strings"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	ailabLong = templates.LongDesc(`
	该工具用于【以动态加载的技能/MCP/知识库增强 AI，然后进行推理】，也可以用来进行 AI 能力的测试。

	【参数说明】：
	- <description>（位置参数，必填）：要处理的问题或指令。
	- --agents / -a：本次使用的 Agent 名称列表。
	- --skills / -s：本次使用的技能名称列表。
	- --mcps：本次使用的 MCP 服务器名称列表。
	- --knowledgebases / -k：本次使用的知识库名称列表。`)

	ailabExample = templates.Examples(`
	# 以动态加载的技能增强后进行推理
	/ailab --skills domain-lead-identifier 帮我查一下这个服务的负责人`)
)

// AILabOptions defines configuration for the "ailab" command.
type AILabOptions struct {
	out runtime.Responder

	Agents         []string
	Skills         []string
	MCPs           []string
	KnowledgeBases []string
	Reasoning      bool
	Debug          bool
}

// NewAILabCmd creates the "ailab" command.
//
// Note: dynamic skill/MCP/knowledge-base loading is not ported to onexai; the
// flags are accepted for schema parity and the question is forwarded to
// oai-agent-apiserver verbatim.
func NewAILabCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &AILabOptions{out: out}

	cmd := &cobra.Command{
		Use:                   "ailab <description>",
		Short:                 "以动态加载的技能/MCP/知识库增强 AI，然后进行推理。也可以用来进行 AI 能力的测试。",
		Long:                  ailabLong,
		Example:               ailabExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.MinimumNArgs(1),
		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "【必填参数】要处理的问题或指令。",
			chat.SREOnly:                     chat.True,
			chat.ChatGroup:                   chat.ChatGroupAI,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd.Context(), args); err != nil {
				return err
			}
			if err := o.Validate(cmd.Context(), args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), sr, runtime.From(cmd.Context()), args)
		},
	}

	cmd.Flags().StringSliceVarP(&o.Agents, "agents", "a", nil, "本次使用的 Agent 名称列表（逗号分隔）")
	cmd.Flags().StringSliceVarP(&o.Skills, "skills", "s", nil, "本次使用的技能名称列表（逗号分隔）")
	cmd.Flags().StringSliceVar(&o.MCPs, "mcps", nil, "本次使用的 MCP 服务器名称列表（逗号分隔）")
	cmd.Flags().StringSliceVarP(&o.KnowledgeBases, "knowledgebases", "k", nil, "本次使用的知识库名称列表（逗号分隔）")
	cmd.Flags().BoolVarP(&o.Reasoning, "reasoning", "r", true, "是否开启深度推理模式")
	cmd.Flags().BoolVar(&o.Debug, "debug", true, "调试模式")

	return cmd
}

// Complete is a no-op for the ailab command.
func (o *AILabOptions) Complete(ctx context.Context, args []string) error {
	return nil
}

// Validate ensures a question is present.
func (o *AILabOptions) Validate(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return runtime.NewUserError("请提供要处理的问题")
	}
	return nil
}

// Run forwards the question to oai-agent-apiserver.
func (o *AILabOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, args []string) error {
	return askViaAgent(ctx, sr, o.out, strings.Join(args, " "))
}
