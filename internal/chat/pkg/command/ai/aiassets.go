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
	"strings"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	aiassetsLong = templates.LongDesc(`
        该工具用于【浏览和查询 AI 能力资源清单】，
        包括 Agent、技能（Skill）、MCP 服务器和知识库（KnowledgeBase）。

        【触发场景】：
        当用户询问"有哪些技能可以用？"、"支持哪些知识库？"、"列出所有 MCP 服务器"、
        "有哪些 Agent 可用？"等问题时，请调用此工具。

        【参数说明】：
        - --agent / -a：列出所有已配置的 Agent
        - --skill / -s：列出所有已配置的技能（Skills）
        - --mcp / -m：列出所有已配置的 MCP 服务器
        - --knowledgebase / -k：列出所有已配置的知识库（KnowledgeBase）
        - 不加任何参数：同时列出 Agent、技能、MCP 服务器和知识库（等同于 -a -s -m -k）

        多个参数可同时使用，例如 --skill --mcp 同时列出技能和 MCP 服务器。`)

	aiassetsExample = templates.Examples(`
        # 列出所有 Agent
        /aiassets --agent

        # 列出所有技能
        /aiassets --skill

        # 列出所有 MCP 服务器
        /aiassets --mcp

        # 列出所有知识库
        /aiassets --knowledgebase

        # 同时列出 Agent 和技能
        /aiassets --agent --skill

        # 列出所有类型（默认行为）
        /aiassets`)
)

// AIAssetsOptions defines configuration for the "aiassets" command.
type AIAssetsOptions struct {
	out runtime.Responder

	Agent         bool
	Skill         bool
	MCP           bool
	KnowledgeBase bool
}

// NewAIAssetsCmd creates the "aiassets" command for browsing AI capability resources.
func NewAIAssetsCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &AIAssetsOptions{out: out}

	cmd := &cobra.Command{
		Use:                   "aiassets [query]",
		Short:                 "浏览或模糊查询已配置的 Agent、技能、MCP 服务器和知识库清单",
		Long:                  aiassetsLong,
		Example:               aiassetsExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.MaximumNArgs(1),
		Annotations: map[string]string{
			chat.SREOnly:                     chat.True,
			cobrax.AnnotationArgPrefix + "0": "可选位置参数，按名称（name）或显示名称（display_name）进行模糊搜索。",
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

	cmd.Flags().BoolVarP(&o.Agent, "agent", "a", false, "列出所有已配置的 Agent")
	cmd.Flags().BoolVarP(&o.Skill, "skill", "s", false, "列出所有已配置的技能（Skills）")
	cmd.Flags().BoolVarP(&o.MCP, "mcp", "m", false, "列出所有已配置的 MCP 服务器")
	cmd.Flags().BoolVarP(&o.KnowledgeBase, "knowledgebase", "k", false, "列出所有已配置的知识库（KnowledgeBase）")

	return cmd
}

// Complete is a no-op for the aiassets command.
func (o *AIAssetsOptions) Complete(ctx context.Context, args []string) error {
	return nil
}

// Validate is a no-op for the aiassets command (a single optional query arg).
func (o *AIAssetsOptions) Validate(ctx context.Context, args []string) error {
	return nil
}

// Run lists the configured AI resources from oai-agent-apiserver and formats them as
// a markdown card.
func (o *AIAssetsOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, args []string) error {
	var query string
	if len(args) > 0 {
		query = strings.ToLower(args[0])
	}

	// No flags specified → show everything.
	showAll := !o.Agent && !o.Skill && !o.MCP && !o.KnowledgeBase

	var sections []string

	if showAll || o.Agent {
		section, err := buildAgentSection(ctx, sr, query)
		if err != nil {
			sections = append(sections, fmt.Sprintf("**Agent** — 获取失败: %v", err))
		} else {
			sections = append(sections, section)
		}
	}
	if showAll || o.Skill {
		section, err := buildSkillSection(ctx, sr, query)
		if err != nil {
			sections = append(sections, fmt.Sprintf("**技能（Skills）** — 获取失败: %v", err))
		} else {
			sections = append(sections, section)
		}
	}
	if showAll || o.MCP {
		section, err := buildMCPSection(ctx, sr, query)
		if err != nil {
			sections = append(sections, fmt.Sprintf("**MCP 服务器** — 获取失败: %v", err))
		} else {
			sections = append(sections, section)
		}
	}
	if showAll || o.KnowledgeBase {
		section, err := buildKBSection(ctx, sr, query)
		if err != nil {
			sections = append(sections, fmt.Sprintf("**知识库（KnowledgeBase）** — 获取失败: %v", err))
		} else {
			sections = append(sections, section)
		}
	}

	return o.out.SendMarkdownCard(ctx, "AI 能力资源清单", strings.Join(sections, "\n\n---\n\n"), "blue")
}

// matches reports whether the item's name or display name contains the query.
func matches(query, name, displayName string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), query) || strings.Contains(strings.ToLower(displayName), query)
}

func buildAgentSection(ctx context.Context, sr *runtime.StaticRuntime, query string) (string, error) {
	agents, err := sr.Clientset.Agent().ListAgent(ctx)
	if err != nil {
		return "", err
	}
	var rows []string
	total := 0
	for _, a := range agents {
		total++
		if matches(query, a.GetName(), a.GetDisplayName()) {
			rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |", a.GetName(), a.GetDisplayName(), a.GetDescription()))
		}
	}
	return renderSection("Agent", query, total, len(rows), rows), nil
}

func buildSkillSection(ctx context.Context, sr *runtime.StaticRuntime, query string) (string, error) {
	skills, err := sr.Clientset.Agent().ListSkill(ctx)
	if err != nil {
		return "", err
	}
	var rows []string
	total := 0
	for _, s := range skills {
		total++
		if matches(query, s.GetName(), s.GetDisplayName()) {
			rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |", s.GetName(), s.GetDisplayName(), s.GetDescription()))
		}
	}
	return renderSection("技能（Skills）", query, total, len(rows), rows), nil
}

func buildMCPSection(ctx context.Context, sr *runtime.StaticRuntime, query string) (string, error) {
	mcps, err := sr.Clientset.Agent().ListMCP(ctx)
	if err != nil {
		return "", err
	}
	var rows []string
	total := 0
	for _, m := range mcps {
		total++
		if matches(query, m.GetName(), m.GetDisplayName()) {
			rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |", m.GetName(), m.GetDisplayName(), m.GetDescription()))
		}
	}
	return renderSection("MCP 服务器", query, total, len(rows), rows), nil
}

func buildKBSection(ctx context.Context, sr *runtime.StaticRuntime, query string) (string, error) {
	kbs, err := sr.Clientset.Agent().ListKnowledgeBase(ctx)
	if err != nil {
		return "", err
	}
	var rows []string
	total := 0
	for _, k := range kbs {
		total++
		if matches(query, k.GetName(), k.GetDisplayName()) {
			rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |", k.GetName(), k.GetDisplayName(), k.GetDescription()))
		}
	}
	return renderSection("知识库（KnowledgeBase）", query, total, len(rows), rows), nil
}

// renderSection formats a single resource category as a markdown table.
func renderSection(title, query string, total, matched int, rows []string) string {
	var sb strings.Builder
	if query != "" {
		sb.WriteString(fmt.Sprintf("**%s** — 筛选词: `%s`, 匹配 %d/%d 条\n\n", title, query, matched, total))
	} else {
		sb.WriteString(fmt.Sprintf("**%s** — 共 %d 条\n\n", title, total))
	}
	if matched == 0 {
		sb.WriteString("没有匹配的记录。\n")
		return sb.String()
	}
	sb.WriteString("| 标识 | 名称 | 描述 |\n")
	sb.WriteString("| --- | --- | --- |\n")
	for _, r := range rows {
		sb.WriteString(r)
		sb.WriteString("\n")
	}
	return sb.String()
}
