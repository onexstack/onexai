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

package duty

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/feizaonet/cobrax"
	"github.com/mattn/go-runewidth"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/chatname"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	oncallLong = templates.LongDesc(`
        该工具用于【创建业务求助工单】或【查询可用的值班模块名单】。

        【模式一：创建工单】
        - 触发场景：当用户明确要求“帮我提个单”、“找一下 xxx 的负责人看一下问题”时。
        - 依赖参数：必须提供 [module]（模块ID）和 [title]（问题描述）。如果用户指定了紧急程度，用 --level 传入。
        - 优先级规则：支持 1、2、3、4（1 最紧急）。用户未说明时务必使用默认值 4。
        - 防幻觉：不知道 [module] 时必须先用【模式二】查询，不要编造模块名；若模块不存在，立即停止建单并告知用户。
        - 未提供描述时可自行补充一个简短的问题描述作为 [title]。

        【模式二：查询值班模块】
        - 触发场景：用户问“有哪些模块可以提单”，或你需要寻找建单模块ID时。
        - 依赖参数：必须开启 --list。此时第一个参数变为[过滤关键词]，按正则匹配模块ID/名称/描述；不传则列出全部。`)

	oncallExample = templates.Examples(`
        # 【查询模式】列出所有的值班模块
        /oncall --list

        # 【查询模式】搜索名称或描述中包含 "moni" 的值班模块
        /oncall --list moni

        # 【建单模式】为模块 "eks" 创建一个 P3 工单
        /oncall -l 3 eks 请帮我看下为什么Pod一直Pending`)
)

// NewOncallCmd returns the /oncall command.
//
//	/oncall <module> <title>   create a ticket for a duty group
//	/oncall --list [filter]    list duty groups
func NewOncallCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	var list bool
	var level int64

	cmd := &cobra.Command{
		Use:                   "oncall [module] [title]",
		Short:                 "创建并提交业务求助工单，或查询支持的值班模块名单",
		Long:                  oncallLong,
		Example:               oncallExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "模式依赖参数。建单模式下必填，代表目标[值班模块ID]；查询模式（--list）下选填，代表过滤模块名称的[关键词/正则]。",
			cobrax.AnnotationArgPrefix + "1": "模式依赖参数。仅建单模式下必填，代表工单的[标题/问题详细描述]。",
			chat.ChatEnabled:                 chat.True,
			chat.ChatGroup:                   chat.ChatGroupDuty,
			chat.MCPTool:                     chat.True,
		},
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOncall(cmd.Context(), sr, out, args, list, level)
		},
	}

	cmd.Flags().BoolVar(&list, "list", false, "【查询模式开关】设为 true 时列出/查询可用的值班模块，不再建单。")
	cmd.Flags().Int64VarP(&level, "level", "l", 4, "【仅建单模式有效】指定工单级别，可选值 1/2/3/4（1 最紧急）；用户未指定时用默认值 4。")
	return cmd
}

func runOncall(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string, list bool, level int64) error {
	dr := runtime.From(ctx)

	if list {
		filter := ""
		if len(args) >= 1 {
			filter = args[0]
		}
		return runListModules(ctx, sr, out, filter)
	}

	if level < 1 || level > 4 {
		return fmt.Errorf("工单级别必须在 1-4 之间，当前值: %d", level)
	}

	// Nothing is loaded at all: there is no picker worth showing and no name
	// that could resolve, so say the service is unavailable instead of opening
	// an empty selection card or blaming the module the user typed. A store
	// whose rosters could not be read is the same dead end for a different
	// reason — it knows the modules but not that any of them is staffed, so the
	// picker would offer the modules and then refuse every one of them.
	known, knownErr := sr.Duty.HasKnownGroups(ctx)
	if knownErr != nil || !known || rosterUnavailable(sr) {
		return runtime.NewUserError("%s", groupServiceUnavailableMessage())
	}

	// No module named: show the picker instead of failing, so a user who does
	// not know the module ids can still raise a ticket.
	if len(args) == 0 {
		return showModuleSelectionCard(ctx, sr, dr, out, level)
	}

	// The picker's last option: the user does not know which module owns the
	// problem, so the backend routes it to a real one. Handled before
	// requireActiveGroup because smart-routing is not a registered module.
	if strings.EqualFold(strings.TrimSpace(args[0]), SmartRoutingModuleID) {
		if len(args) < 2 {
			return runtime.NewUserError("请输入工单标题，例如：/oncall %s <工单标题，允许空格>", SmartRoutingModuleID)
		}
		title := strings.Join(args[1:], " ")
		resolved, err := resolveSmartRoutingModule(ctx, sr, title)
		if err != nil {
			return err
		}
		return createTicketForModule(ctx, sr, dr, out, resolved, title, level)
	}

	// A lone token that names no known module is a title, not a module: the
	// user typed a description like "/oncall 咨询下Go开发" (consult on Go development) and left the module to
	// the backend. Multi-token input stays strict on purpose — a typo'd module
	// name followed by a title must be reported, never silently routed to some
	// unrelated group.
	//
	// GetGroupInfo, not HasGroup: a registered module with no rotation right
	// now is still "known", so "/oncall go" must keep answering "请输入工单标题" (enter the ticket title).
	if len(args) == 1 {
		if token := args[0]; token != "" {
			_, ok, lookupErr := sr.Duty.GetGroupInfo(ctx, strings.ToLower(token))
			if lookupErr != nil {
				return runtime.NewUserError("%s", groupServiceUnavailableMessage())
			}
			if !ok {
				resolved, err := resolveSmartRoutingModule(ctx, sr, token)
				if err != nil {
					return err
				}
				return createTicketForModule(ctx, sr, dr, out, resolved, token, level)
			}
		}
	}

	group, err := requireActiveGroup(ctx, sr, args[0])
	if err != nil {
		return err
	}
	module := group.ID
	if len(args) < 2 {
		return runtime.NewUserError("请输入工单标题，例如：/oncall %s <工单标题，允许空格>", module)
	}
	title := strings.Join(args[1:], " ")
	return createTicketForModule(ctx, sr, dr, out, module, title, level)
}

// runListModules lists the active duty modules, optionally filtered by a
// case-insensitive regular expression, as a fixed-width three-column table.
func runListModules(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, filter string) error {
	items, ok, err := sr.Duty.FilterGroups(ctx, filter)
	if err != nil {
		return runtime.NewUserError("%s", groupServiceUnavailableMessage())
	}
	if !ok {
		return runtime.NewUserError("无效的正则表达式 '%s'", filter)
	}
	if len(items) == 0 {
		// An empty store is not "no modules match": nothing was loaded, so the
		// listing cannot be trusted and must not be presented as authoritative.
		// The same goes for a store whose rosters could not be read: it knows
		// the modules, but not that any of them is staffed.
		known, knownErr := sr.Duty.HasKnownGroups(ctx)
		if knownErr != nil || !known || rosterUnavailable(sr) {
			return out.SendText(ctx, groupServiceUnavailableMessage())
		}
		message := "当前没有可用的值班模块"
		if filter != "" {
			message = fmt.Sprintf("未找到匹配 '%s' 的值班模块", filter)
		}
		return out.SendText(ctx, message)
	}

	codeWidth, nameWidth := runewidth.StringWidth("模块ID"), runewidth.StringWidth("模块名称")
	for _, g := range items {
		if w := runewidth.StringWidth(g.ID); w > codeWidth {
			codeWidth = w
		}
		if w := runewidth.StringWidth(g.Name); w > nameWidth {
			nameWidth = w
		}
	}

	var sb strings.Builder
	title := fmt.Sprintf("共找到 %d 个值班模块\n", len(items))
	sb.WriteString("```\n")
	sb.WriteString(formatThreeColumns("模块ID", "模块名称", "模块描述", codeWidth, nameWidth))
	sb.WriteString(formatThreeColumns(
		strings.Repeat("-", codeWidth),
		strings.Repeat("-", nameWidth),
		strings.Repeat("-", 35),
		codeWidth, nameWidth,
	))
	for _, g := range items {
		desc := g.ShortDesc
		if desc == "" {
			desc = g.LongDesc
		}
		desc = strings.NewReplacer("\n", " ", "\r", " ").Replace(desc)
		sb.WriteString(formatThreeColumns(g.ID, g.Name, desc, codeWidth, nameWidth))
	}
	sb.WriteString("```")

	raw := make([]*v1.OnDutyGroup, 0, len(items))
	for _, g := range items {
		raw = append(raw, &v1.OnDutyGroup{
			GroupId:   g.ID,
			GroupName: g.Name,
			ShortDesc: g.ShortDesc,
			LongDesc:  g.LongDesc,
		})
	}

	return out.SendResult(ctx, &runtime.CommandResult{
		Raw: raw,
		Markdown: &runtime.MarkdownContent{
			Title:   title,
			Content: sb.String(),
		},
		PlainText: title + "\n" + sb.String(),
	})
}

// formatThreeColumns renders one left-aligned line of a fixed-width table.
func formatThreeColumns(col1, col2, col3 string, width1, width2 int) string {
	const gap = 4
	pad := func(s string, width int) string {
		if n := width - runewidth.StringWidth(s); n > 0 {
			return s + strings.Repeat(" ", n)
		}
		return s
	}
	return fmt.Sprintf("%s%s%s%s%s\n",
		pad(col1, width1), strings.Repeat(" ", gap),
		pad(col2, width2), strings.Repeat(" ", gap),
		col3,
	)
}

// showModuleSelectionCard sends the flat module-picker card. The template
// renders one select option per module and submits a title form.
func showModuleSelectionCard(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, out runtime.Responder, level int64) error {
	modules := make([]map[string]string, 0)
	infos, err := sr.Duty.ActiveGroupInfos(ctx)
	if err != nil {
		return runtime.NewUserError("%s", groupServiceUnavailableMessage())
	}
	for _, g := range infos {
		text := fmt.Sprintf("%s | %s", g.ID, g.Name)
		if g.ShortDesc != "" {
			text = fmt.Sprintf("%s | %s", text, g.ShortDesc)
		}
		modules = append(modules, map[string]string{
			"text":  text,
			"value": g.ID,
		})
	}

	// smart-routing is a synthetic option pinned to the bottom of the list: it
	// is not a real module, so it is appended after the sorted modules rather
	// than registered in the store. Selecting it asks the backend to match a
	// real module with the LLM.
	modules = append(modules, map[string]string{
		"text":  fmt.Sprintf("%s | 由 OneX 自动匹配最合适的值班组", SmartRoutingModuleID),
		"value": SmartRoutingModuleID,
	})

	chatType := ""
	sessionKey := ""
	if dr != nil && dr.Receiver != nil {
		chatType = dr.ChatType()
		sessionKey = dr.Address()
	}

	return out.SendResult(ctx, &runtime.CommandResult{
		LarkTemplateID: chat.OncallFlatSelectCardID,
		// A surface without a template renderer (the card menu, the TUI) falls
		// back to this; the Lark path sends the card and ignores it.
		PlainText: "请选择要建单的值班模块。",
		LarkVariables: map[string]any{
			"chat_type":     chatType,
			"default_level": level,
			"modules":       modules,
			// Template cards do not get the session key injected the way a
			// larkcard.Interactive card does, so it is passed explicitly; the
			// callback falls back to chat_type/open_chat_id when it is absent.
			"session_key": sessionKey,
			// The template also renders an integration/usage doc link; this
			// deployment has no equivalent wiki page, so both are left blank and
			// the template shows nothing for them.
			"bot_integration_doc": "",
			"bot_usage_doc":       "",
		},
	})
}

// createTicketForModule creates a ticket for the given duty group.
func createTicketForModule(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, out runtime.Responder, module, title string, level int64) error {
	group, ok, err := sr.Duty.GetGroupInfo(ctx, module)
	if err != nil {
		return runtime.NewUserError("%s", groupServiceUnavailableMessage())
	}
	if !ok {
		return runtime.NewUserError("未找到模块 '%s'，请使用 `/oncall --list` 查看可用模块列表", module)
	}
	// A module with no watcher is not a module that is missing — the picker
	// just offered it. Say which of the two it is, so a roster outage does not
	// read as a typo in the module id.
	if len(group.Watchers) == 0 {
		if rosterUnavailable(sr) {
			return runtime.NewUserError("%s", groupServiceUnavailableMessage())
		}
		return runtime.NewUserError("未找到模块 '%s'，请使用 `/oncall --list` 查看可用模块列表", module)
	}
	if dr == nil || !dr.HasLarkContext() {
		return runtime.NewUserError("当前会话没有可用的 Lark 上下文，无法创建工单群")
	}

	// The ticket group is created with user_ids, so the opener has to have one:
	// handing Lark the sender's open_id here is what made /oncall fail with
	// "not a valid {user_id}" for every ticket ever raised.
	senderUserID := dr.Identity()
	if senderUserID == "" {
		return runtime.NewUserError("无法识别你的飞书 user_id，暂时无法创建工单群（请在应用后台确认已开启「获取用户 user ID」权限）")
	}

	primary := group.Primary()
	tempTitle := chatname.FormatName(chat.StatusProcessing, level, title)

	chatID, err := dr.LarkServices.CreateChat(ctx, senderUserID, tempTitle, primary)
	if err != nil {
		return err
	}

	// The reporter's display name needs a contact lookup, which can fail or be
	// slow; it is best-effort and an empty answer is stored as-is. The open_id
	// beside it needs no lookup at all — it is on the runtime already, on this
	// path and on the card path alike (see DynamicRuntime.Handle).
	if _, err := createTicket(
		ctx, sr, chatID, group.ID, title, level,
		senderUserID, callerName(ctx, dr), dr.Handle(),
	); err != nil {
		return err
	}

	// The group-join announcement is the one thing the duty-group row promises
	// that no other step delivers: the chat is created empty otherwise. It is
	// sent to the new chat, not to the receiver the command came from, and a
	// failure is logged rather than failing a ticket that already exists.
	if welcome := strings.TrimSpace(group.WelcomeMessage); welcome != "" {
		recv := &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: chatID}
		if err := dr.LarkServices.SendMarkdownCard(ctx, recv, "", welcome, "blue"); err != nil {
			slog.WarnContext(ctx, "oncall: could not post the welcome message to the ticket group",
				"chat_id", chatID, "group", group.ID, "error", err)
		}
	}

	if dr.IsCardAction {
		return nil
	}

	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       map[string]any{"lark_chat_id": chatID},
		Markdown:  &runtime.MarkdownContent{Content: fmt.Sprintf("工单创建成功！请在群 **%s** 中沟通、处理问题。", tempTitle)},
		PlainText: fmt.Sprintf("工单创建成功！请在群 %s 中沟通、处理问题。", tempTitle),
	})
}
