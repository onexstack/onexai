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

// Package core holds the chat commands that describe the service itself rather
// than do work in a conversation.
package core

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/i18n"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	doctorLong = templates.LongDesc(`
        自检：报告这台机器上的服务是怎么配置的、正在跑什么，并指出值得注意的地方。

        【触发场景】：
        - "服务正常吗"、"机器人怎么不回我"、"现在配了什么"
        - 开启本机 Agent 后端前后，确认权限与限制符合预期

        【报告内容】：
        - 会话目录、会话数量、后端分布
        - 已启用的 Agent 后端与默认后端
        - 流式输出与去重窗口
        - 授权人数、入站限制是否设置

        【为什么仅管理员、仅私聊】：
        报告描述的是机器人所在主机与整个部署的配置，不属于聊天内容。`)

	doctorExample = templates.Examples(`
        # 看看服务当前的配置与运行情况
        /doctor`)
)

// NewDoctorCmd creates the "doctor" command.
func NewDoctorCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	return &cobra.Command{
		Use:                   "doctor",
		Aliases:               []string{"health", "selfcheck"},
		Short:                 "服务自检：报告当前配置、运行状态与需要留意的地方",
		Long:                  doctorLong,
		Example:               doctorExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Annotations: map[string]string{
			chat.ChatGroup: chat.ChatGroupCore,
			chat.AdminOnly: chat.True,
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd.Context(), sr, runtime.From(cmd.Context()), out)
		},
	}
}

// runDoctor assembles the report.
//
// Everything here is local: no network call, no process started. A diagnostic
// that can hang on the thing it is diagnosing is worse than no diagnostic, and
// the questions worth answering — which backends exist, what is limited, who may
// run what — are all answerable from what the runtime already holds.
func runDoctor(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, out runtime.Responder) error {
	if sr == nil {
		return runtime.NewUserError("服务未初始化，无法自检")
	}
	// Doctor reports on the deployment, not on a chat, for the same reason
	// /sessions does: the answers describe the host.
	if dr == nil || dr.Receiver == nil || !dr.IsP2P() {
		return sr.UserError("该命令只能在私聊中使用：它报告的是机器人所在主机与部署的配置")
	}

	var (
		b        strings.Builder
		findings []string
	)

	b.WriteString(sr.Translate("**服务自检**\n"))
	b.WriteString(doctorLanguage(sr))
	b.WriteString(doctorSessions(sr, &findings))
	b.WriteString(doctorBackends(sr, &findings))
	b.WriteString(doctorStreaming(sr, &findings))
	b.WriteString(doctorAccess(sr, &findings))
	b.WriteString(doctorDependencies(sr, &findings))

	if len(findings) == 0 {
		b.WriteString("\n")
		b.WriteString(sr.Translate("没有发现需要注意的问题。"))
	} else {
		b.WriteString("\n")
		b.WriteString(sr.Translate("**需要注意**\n"))
		for _, f := range findings {
			b.WriteString(sr.Translate("- %s\n", f))
		}
	}

	return out.SendResult(ctx, &runtime.CommandResult{PlainText: b.String()})
}

// doctorLanguage reports the language replies are written in.
//
// It is in the report because the translation catalog is partial: a string that
// has no translation is sent as written, so a deployment that chose a language
// and still sees Chinese is looking at untranslated coverage rather than at a
// broken setting. Saying which language is in effect is what makes the two
// distinguishable.
func doctorLanguage(sr *runtime.StaticRuntime) string {
	lang := sr.Language
	if lang == "" {
		lang = i18n.Default
	}
	// The effective language is named by its own tag, not translated: a report
	// that says "Language: 中文" (Chinese) to an English reader has answered the wrong
	// question.
	if lang == i18n.ZH {
		return fmt.Sprintf("- 语言：%s（源语言；未翻译的文案按原文发送）\n", lang)
	}
	return fmt.Sprintf("- Language: %s (untranslated strings are sent as written)\n", lang)
}

// doctorSessions reports the conversation store.
func doctorSessions(sr *runtime.StaticRuntime, findings *[]string) string {
	if sr.Sessions == nil {
		*findings = append(*findings, sr.Translate("会话管理未启用：/new、/switch、排队与空闲回收都不可用"))
		return sr.Translate("- 会话：未启用\n")
	}

	live := sr.Sessions.Sessions()
	var busy, pending int
	counts := map[string]int{}
	for _, sess := range live {
		counts[sess.State().Backend]++
		if sess.Busy() {
			busy++
		}
		if sess.PendingPermission() != nil {
			pending++
		}
	}

	var b strings.Builder
	b.WriteString(sr.Translate("- 会话目录：%s\n", sr.Sessions.Directory()))
	b.WriteString(sr.Translate("- 会话：%d 个在运行（%d 个处理中，%d 个等待审批）\n", len(live), busy, pending))
	for _, backend := range sortedBackends(counts) {
		b.WriteString(sr.Translate("  - %s：%d 个\n", backend, counts[backend]))
	}
	if d := sr.Sessions.TurnTimeout(); d > 0 {
		b.WriteString(sr.Translate("- 单轮上限：%s\n", d))
	}
	return b.String()
}

// doctorBackends reports which agent backends are registered.
func doctorBackends(sr *runtime.StaticRuntime, findings *[]string) string {
	if sr.Agents == nil {
		*findings = append(*findings, sr.Translate("没有注册任何 Agent 后端：AI 对话不可用"))
		return sr.Translate("- Agent 后端：无\n")
	}

	names := sr.Agents.Names()
	sort.Strings(names)
	if len(names) == 0 {
		*findings = append(*findings, sr.Translate("没有注册任何 Agent 后端：AI 对话不可用"))
		return sr.Translate("- Agent 后端：无\n")
	}

	var b strings.Builder
	b.WriteString(sr.Translate("- Agent 后端：%s（默认 %s）\n", strings.Join(names, "、"), sr.Agents.Fallback()))
	if _, ok := sr.Agents.Get(agent.BackendCLI); ok {
		// The one configuration that turns a chat message into a process on this
		// host. That it is on is worth stating on every report, not only when it
		// is misconfigured.
		b.WriteString(sr.Translate("  - cli 后端已启用：会话消息可在本机运行 agent 进程，仅管理员可切换\n"))
	}
	if _, ok := sr.Agents.Get(agent.BackendLLM); !ok && sr.Agents.Fallback() == agent.BackendLLM {
		*findings = append(*findings, sr.Translate("默认后端是 llm，但它没有注册：未显式切换后端的会话都会失败"))
	}
	return b.String()
}

// doctorStreaming reports the outbound throttling configuration.
func doctorStreaming(sr *runtime.StaticRuntime, findings *[]string) string {
	var b strings.Builder
	if !sr.StreamingEnabled {
		b.WriteString(sr.Translate("- 流式输出：关闭（回答在结束时一次性发出）\n"))
	} else {
		b.WriteString(sr.Translate("- 流式输出：开启（间隔 %s，最小增量 %d 字符，预览上限 %d 字符）\n",
			sr.Streaming.Interval, sr.Streaming.MinDeltaChars, sr.Streaming.MaxChars))
		if sr.Streaming.MinDeltaChars <= 1 && sr.Streaming.Interval == 0 {
			*findings = append(*findings, sr.Translate("流式节流已关闭：每个增量都会触发一次卡片更新，可能触及飞书的更新频率上限"))
		}
	}
	if sr.MaxEventAge > 0 {
		b.WriteString(sr.Translate("- 消息去重窗口：%s\n", sr.MaxEventAge))
	}
	return b.String()
}

// doctorAccess reports who may run what, and which inbound messages are allowed.
//
// It reports counts rather than ids: this is a health report, and a list of
// operators in a chat log outlives the question that produced it.
//
// The lists are read from chat.setting and cached, so when they were last read
// and whether the last read succeeded are part of the answer: the lists are
// fail-closed, and a refresh that has been failing for an hour is the difference
// between "nobody is an administrator" and "nobody can become one right now".
func doctorAccess(sr *runtime.StaticRuntime, findings *[]string) string {
	roles := sr.Roles.Status()

	var b strings.Builder
	b.WriteString(sr.Translate("- 授权：SRE %d 人，管理员 %d 人\n", roles.SRE, roles.Admin))
	if !roles.LoadedAt.IsZero() {
		b.WriteString(sr.Translate("- 角色名单读取于 %s\n", roles.LoadedAt.Format("2006-01-02 15:04:05")))
	}
	if roles.Err != nil {
		b.WriteString(sr.Translate("- 名单最近一次刷新失败：%s（仍在使用上一次成功读取的名单）\n", roles.Err))
	}

	if roles.Admin == 0 {
		if _, ok := sr.Agents.Get(agent.BackendCLI); ok {
			*findings = append(*findings, sr.Translate("已启用 cli 后端但管理员名单为空：没有人能用 /new 或 /switch 切到它"))
		} else {
			*findings = append(*findings, sr.Translate("管理员名单为空：/new、/switch、/sessions、/doctor 无人可用"))
		}
	}
	if roles.SRE == 0 {
		*findings = append(*findings, sr.Translate("SRE 名单为空：所有 x-sre-only 命令无人可用"))
	}

	b.WriteString(sr.Translate("- 入站限制："))
	b.WriteString(allowSummary(sr))
	b.WriteString("\n")
	if _, ok := sr.Agents.Get(agent.BackendCLI); ok && sr.AllowFrom == nil && sr.AllowChat == nil {
		*findings = append(*findings,
			sr.Translate("已启用 cli 后端且未设置来源/群聊白名单：任何能私聊机器人的人都能驱动本机 agent（命令本身仍限管理员）"))
	}
	return b.String()
}

// allowSummary describes the inbound allow-lists. A nil set means "allow all",
// which is what the empty configuration intends and is worth stating plainly.
func allowSummary(sr *runtime.StaticRuntime) string {
	var parts []string
	if sr.AllowFrom == nil {
		parts = append(parts, sr.Translate("来源不限"))
	} else {
		parts = append(parts, sr.Translate("来源 %d 项", len(sr.AllowFrom)))
	}
	if sr.AllowChat == nil {
		parts = append(parts, sr.Translate("群聊不限"))
	} else {
		parts = append(parts, sr.Translate("群聊 %d 项", len(sr.AllowChat)))
	}
	if sr.GroupOnly {
		parts = append(parts, sr.Translate("仅群聊"))
	}
	if sr.ReplyToTrigger {
		parts = append(parts, sr.Translate("仅回复触发消息"))
	}
	return strings.Join(parts, "，")
}

// doctorDependencies reports the optional pieces that are configured or missing.
func doctorDependencies(sr *runtime.StaticRuntime, findings *[]string) string {
	var b strings.Builder

	b.WriteString(sr.Translate("- oai-agent-apiserver："))
	if sr.Clientset == nil || sr.Clientset.Agent() == nil {
		b.WriteString(sr.Translate("未配置\n"))
		*findings = append(*findings, sr.Translate("未配置 oai-agent-apiserver：llm 后端不可用，/clear、Agent 列表等命令也会失败"))
	} else {
		b.WriteString(sr.Translate("已配置\n"))
	}

	b.WriteString(sr.Translate("- 数据库："))
	if sr.Store == nil {
		b.WriteString(sr.Translate("未配置\n"))
	} else {
		b.WriteString(sr.Translate("已配置\n"))
	}

	b.WriteString(sr.Translate("- DNS 白名单："))
	if sr.DNSWhitelist == nil {
		b.WriteString(sr.Translate("未加载\n"))
	} else {
		b.WriteString(sr.Translate("已加载\n"))
	}

	return b.String()
}

// sortedBackends returns the keys of counts in a fixed order.
func sortedBackends(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
