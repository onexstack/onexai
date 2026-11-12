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
	"log/slog"
	"strings"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/agentref"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	agentLong = templates.LongDesc(`
        该工具用于【在当前 Lark 群中加载/调度指定的 Agent 能力，构建出一个增强 Runner，
        并将其关联到当前工单群，使后续的对话自动使用这些加载的能力】。

        【与 /ailab 的区别】：
        - /ailab：临时加载资源仅用于单次问答，不修改会话的 Agent 配置
        - /agent：将加载结果持久化到当前会话的 DB，且具备自动补齐会话记录的能力

        【私聊中的优先级】：
        私聊以你飞书账号绑定的智能体为准；本命令写入的声明仅在未绑定、或绑定没有可用智能体时生效。

        【触发场景】：
        当用户需要在群聊中引入某个特定的 Agent 时，如：
        - "在这个群里加载 bke-agent"
        - "用覆盖模式加载 monitor-agent"
        - "重新指定这个群的 Agent 为 xxx"

        【执行行为】：
        - 默认模式（合并）：将新的 Agent 追加合并到群里已有的能力中。
        - 覆盖模式（使用 -o 或 --override）：直接使用指定的 Agent 覆盖群内原有的 Agent（系统底层的内置 Agent 会自动保留）。

        【参数说明】：
        - <agent_id...>（位置参数，必填）：一个或多个 Agent ID，空格分隔。
          系统将自动加载其关联的技能（Skills）、MCP 服务器和知识库（KnowledgeBases）。`)

	agentExample = templates.Examples(`
        # 合并模式（默认）：在现有能力基础上追加 bke-agent
        /agent bke-agent

        # 覆盖模式：覆盖当前群的所有额外能力，仅保留 monitor-agent 和基础能力
        /agent --override monitor-agent

        # 合并模式：同时追加多个 Agent（空格分隔）
        /agent bke-agent sre-agent`)
)

// AgentOptions defines configuration for the "agent" command.
type AgentOptions struct {
	out runtime.Responder

	Override bool     // Override mode: replace rather than merge
	Reset    bool     // Clear the declaration and fall back to the default for the conversation type
	Agents   []string // Agent references to load
}

// NewAgentCmd creates the "agent" command.
func NewAgentCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &AgentOptions{out: out}

	cmd := &cobra.Command{
		Use:                   "agent [<ref>...]",
		Short:                 "指定本会话使用的 Agent（管理员）",
		Long:                  agentLong,
		Example:               agentExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		// ArbitraryArgs rather than MinimumNArgs(1): --reset takes no refs, and
		// the rule that a run must carry either refs or --reset belongs in
		// Validate, where the flag is already parsed.
		Args: cobra.ArbitraryArgs,
		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "一个或多个 Agent 名或引用（如 ops-agent、@peer）。",
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

	cmd.Flags().BoolVarP(&o.Override, "override", "o", false,
		"覆盖模式：替换本会话已有的声明（不带此参数则合并追加）")
	cmd.Flags().BoolVar(&o.Reset, "reset", false,
		"清空本会话的声明，改回跟随会话类型的默认 Agent")

	return cmd
}

// Complete captures the positional references.
func (o *AgentOptions) Complete(_ context.Context, args []string) error {
	o.Agents = args
	return nil
}

// Validate ensures the run carries either references or --reset.
func (o *AgentOptions) Validate(_ context.Context, args []string) error {
	if o.Reset && len(args) > 0 {
		return runtime.NewUserError("--reset 不接受 Agent 参数：它把本会话改回默认")
	}
	if !o.Reset && len(args) == 0 {
		return runtime.NewUserError("必须指定至少一个 Agent（名字或引用），或使用 --reset 改回默认")
	}
	return nil
}

// Run validates the requested references and records them as the session's
// declaration.
//
// # What this does and does not verify
//
// It verifies, per entry: that it parses, that a reference names a kind this
// build implements, that a literal is a legal agent name, that the agent exists,
// and that the caller may use it. It does **not** resolve a reference — that is
// an intent about who will be speaking, and nobody knows that yet.
//
// # A bad entry is skipped, not fatal (decided 2026-10-07)
//
// The run is per entry. An entry that fails its check is dropped and named in
// the reply; the entries that passed are written. Only a run where **nothing**
// survived writes nothing, and that is reported as a refusal.
//
// The alternative — refusing the whole run when one entry is bad — makes a
// single misspelling cost the caller every other entry they typed. The risk it
// was meant to cover is covered anyway: nothing is written silently, the card
// lists what was skipped and why, and the scheduler's answer is unchanged
// because a declaration that lost an entry simply resolves to fewer candidates.
//
// # Why the caller must be an administrator
//
// The declaration decides which agent answers *everyone* in this conversation.
// Anyone in a group can type a command, so without this check any member could
// point the conversation at an agent of their choosing, and the answers would
// then be scoped by whatever identity the next speaker happens to carry.
func (o *AgentOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, _ []string) error {
	if !dr.HasLarkContext() {
		return runtime.NewUserError("/agent 只能在 Lark 会话中调用")
	}
	if !sr.IsAdminUser(dr.Identity()) {
		return runtime.NewUserError("/agent 只能由管理员执行：它决定本会话所有人的回答由哪个 Agent 驱动")
	}
	if dr.SessionM == nil {
		// The session row is where a declaration lives, and it only exists once
		// the bot has seen a message here. Saying so is more useful than writing
		// into a row that is not there and reporting success.
		return runtime.NewUserError("/agent 需要本会话已有一条记录：请先在本会话里发一条消息，再执行 /agent")
	}

	if o.Reset {
		if err := sr.Store.Session().SetRefs(ctx, dr.SessionM.SessionID, ""); err != nil {
			return err
		}
		// Keep the in-memory snapshot in step, for the same reason the merge path
		// below does: the rest of this turn reads dr, and a scheduler that ran
		// after a reset would otherwise resolve the declaration that was just
		// cleared.
		dr.SessionM.Refs = ""
		return o.out.SendResult(ctx, &runtime.CommandResult{
			Markdown: &runtime.MarkdownContent{
				Title:   "已改回默认",
				Content: "本会话不再指定 Agent，改回跟随会话类型的默认。" + o.resolutionNote(ctx, dr),
				Color:   "green",
			},
			PlainText: "已改回默认",
		})
	}

	// The permission question is asked about the identity a run will be
	// authorized as, not about the raw sender. A speaker whose Lark account is
	// bound to a platform account is authorized as that account — that is what
	// the rules name, and what the scheduler hands the agent service — so asking
	// about the tenant user_id here would refuse an agent the run itself allows.
	subject, err := o.subjectFor(ctx, sr, dr)
	if err != nil {
		return runtime.NewUserError("无法确定本会话的授权主体：%v", err)
	}

	var valid, skipped []string
	for _, raw := range o.Agents {
		if raw == "" {
			continue
		}
		ref, err := agentref.ParseRef(raw)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s（%s）", raw, err.Error()))
			continue
		}
		if ref.IsRef() {
			// A reference is an intent, so there is no name to check — only that
			// some decider will claim it. Checking the kind is what turns a typo
			// into an immediate refusal rather than an error on the next message.
			if !agentref.KnownKind(ref.Kind) {
				skipped = append(skipped, fmt.Sprintf("%s（未知的引用类型 %q，可用：%s）",
					raw, ref.Kind, strings.Join(agentref.Kinds(), ", ")))
				continue
			}
			valid = append(valid, ref.String())
			continue
		}
		if err := agentref.ValidateAgentName(ref.Literal); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s（%s）", raw, err.Error()))
			continue
		}
		if _, err := sr.Clientset.Agent().GetAgent(ctx, ref.Literal); err != nil {
			skipped = append(skipped, raw)
			continue
		}
		if allowed, reason, err := sr.Clientset.Agent().CheckAgentAccess(ctx, subject, ref.Literal); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s（授权判定失败：%s）", raw, err.Error()))
			continue
		} else if !allowed {
			skipped = append(skipped, fmt.Sprintf("%s（无权限：%s）", raw, reason))
			continue
		}
		valid = append(valid, ref.Literal)
	}

	if len(valid) == 0 {
		return runtime.NewUserError("没有任何条目可用，本会话的声明未改动：%s", strings.Join(skipped, "、"))
	}

	// Merging is by reference string, not by resolved agent: two references that
	// happen to resolve to the same agent today are still two intentions, and
	// collapsing them would freeze one of them into the other's answer.
	existing := dr.SessionM.RefList()
	if o.Override {
		existing = nil
	}
	merged := mergeRefs(existing, valid)

	if err := sr.Store.Session().SetRefs(ctx, dr.SessionM.SessionID, model.EncodeNameList(merged)); err != nil {
		return err
	}
	// Keep the in-memory snapshot in step so the summary below, and anything
	// else reading dr in this turn, sees what was just written.
	dr.SessionM.Refs = model.EncodeNameList(merged)

	summary := o.buildSummary(merged)
	if len(skipped) > 0 {
		summary = fmt.Sprintf("%s\n\n*(以下条目被跳过：%s)*", summary, strings.Join(skipped, "、"))
	}
	summary += o.resolutionNote(ctx, dr)
	return o.out.SendResult(ctx, &runtime.CommandResult{
		Markdown:  &runtime.MarkdownContent{Title: "Agent 声明已更新", Content: summary, Color: "green"},
		PlainText: fmt.Sprintf("Agent 声明已更新: %s", strings.Join(merged, ", ")),
	})
}

// subjectFor resolves the identity the agent service will authorize this
// speaker's runs as, by looking up their account binding.
//
// It reads the same row, through the same accessor, that the scheduler reads
// when it builds a run, and chooses with the same function
// (runtime.SubjectFor) — because the two must agree. The permission question
// below is therefore asked about the same subject the run will be, instead of
// about the raw sender, which differs whenever the speaker has bound their Lark
// account to a platform account.
//
// A lookup that failed is an error rather than a fallback: answering with the
// tenant identity would deny an agent the run allows, and the speaker would be
// told they may not use something they were using.
func (o *AgentOptions) subjectFor(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime) (string, error) {
	tenantUserID := dr.Identity()
	if sr == nil || sr.Store == nil || dr.Handle() == "" {
		return tenantUserID, nil
	}
	peers := sr.Store.PeerAgents()
	if peers == nil {
		return tenantUserID, nil
	}
	row, err := peers.GetByPeer(ctx, chat.BackendLark, sr.AppID, dr.Handle())
	if err != nil {
		return "", fmt.Errorf("查询账号绑定失败: %w", err)
	}
	return runtime.SubjectFor(tenantUserID, row), nil
}

// resolutionNote asks the deployment what the declaration it has just written
// resolves to right now, and renders the answer as a line for the reply.
//
// # Why the command cannot work this out itself
//
// A declaration is an intent about who will be speaking, so its effect is
// invisible from here: `@peer` means one thing to somebody who has bound an
// account and another to somebody who has not, and a static name means nothing
// until an authorization decides whether *this* speaker may use it. Only the
// scheduler can say, and it lives behind the DynamicRuntime callback — the bot
// hands it over because the bot imports this package and not the other way
// round.
//
// Saying which of those applies, while the person who typed the command is
// still looking at the reply, is the difference between "the command worked"
// and "the command did what I meant".
//
// The wording is AgentResolution.Line rather than a sentence produced by the
// bot, which is what the callback used to carry: the card that shows the same
// thing (a conversation's agent card) renders that same Line, so the two cannot
// drift apart. A failed resolution says nothing rather than an error — the
// declaration is stored either way, and the next message will report the same
// failure to a reader who is expecting an answer.
func (o *AgentOptions) resolutionNote(ctx context.Context, dr *runtime.DynamicRuntime) string {
	if dr == nil || dr.ResolvedAgents == nil {
		return ""
	}
	res, err := dr.ResolvedAgents(ctx, dr)
	if err != nil {
		slog.WarnContext(ctx, "could not resolve what the declaration that was just written picks", "error", err)
		return ""
	}
	note := "\n\n> " + res.Line()
	if res.BindingPreferred {
		note += "\n\n> 私聊里，你飞书账号的绑定优先于本会话的声明；解绑或清空绑定后，这份声明才会生效。"
	}
	return note
}

// mergeRefs appends requested to existing, keeping the first occurrence's
// position and dropping repeats.
//
// Order is meaning: the scheduler takes the first candidate, so the earliest
// entry is the one that answers when several resolve.
func mergeRefs(existing, requested []string) []string {
	seen := make(map[string]bool, len(existing)+len(requested))
	out := make([]string, 0, len(existing)+len(requested))
	for _, n := range append(append([]string{}, existing...), requested...) {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func (o *AgentOptions) buildSummary(refs []string) string {
	modeStr := "合并"
	if o.Override {
		modeStr = "覆盖"
	}
	return fmt.Sprintf("本会话的 Agent 声明已以**%s模式**更新为：%s\n"+
		"后续本会话按这个顺序解析（第一个能解析出来的生效）。执行 `/agent --reset` 可改回默认。",
		modeStr, formatList(refs))
}

// formatList renders a string slice as a markdown-friendly cell value.
func formatList(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "`" + s + "`"
	}
	return strings.Join(quoted, ", ")
}
