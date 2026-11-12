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
	"time"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	assignLong = templates.LongDesc(`
        该工具用于【将当前工单及处理权指派（转交）给具体的某个人】。
        当用户意图包含“把这个工单转给张三”、“指派给 @xxx”、“让 xxx 来处理这个单子”时调用。

        【与 /hand 的严格区分】：
        - 转交给某个【业务模块/组】（如 bke、dba）→ 用 /hand。
        - 转交给某个【具体的个人】（如 @张三）→ 用本工具 (/assign)。

        【自动执行的后台操作】：
        - 将目标人员邀请进当前 Lark 工单群。
        - 将工单处理人变更为该人员。
        - 在群内发送人员交接通知。

        【参数规则】：必须且只能 @ 一个人；不支持同时指派给多人。`)

	assignExample = templates.Examples(`
        # 将工单转交给具体的个人
        /assign @张三`)
)

// NewAssignCmd returns the /assign command.
func NewAssignCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	return &cobra.Command{
		Use:                   "assign <lark_username>",
		Short:                 "将当前工单指派给 @ 的人员处理",
		Long:                  assignLong,
		Example:               assignExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.MinimumNArgs(1),
		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "被 @ 的目标人员。",
			chat.ChatEnabled:                 chat.True,
			chat.ChatGroup:                   chat.ChatGroupDuty,
			chat.ChatOnly:                    chat.True,
			chat.SREOnly:                     chat.True,
			chat.MCPTool:                     chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAssign(cmd.Context(), sr, out, runtime.From(cmd.Context()))
		},
	}
}

func runAssign(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, dr *runtime.DynamicRuntime) error {
	if !dr.HasLarkContext() {
		return runtime.NewUserError("此命令只能在工单群中执行")
	}

	// Drop the bot's own mention: a user often writes "@Helper /assign @张三" (@Zhang San).
	mentions := make([]runtime.MentionedUser, 0, len(dr.Mentions))
	for _, m := range dr.Mentions {
		if m.IsBot {
			continue
		}
		mentions = append(mentions, m)
	}
	if len(mentions) != 1 {
		return runtime.NewUserError("移交人为空或者为多人，只能移交给一个负责人")
	}
	newPrimary := mentions[0]

	if err := dr.LarkServices.InviteMembers(ctx, dr.Address(), "user_id", []string{newPrimary.UserID}); err != nil {
		return err
	}

	ticket, err := activeTicket(ctx, sr, dr)
	if err != nil {
		return err
	}

	ticket.AssigneeID = newPrimary.UserID
	ticket.AssigneeName = newPrimary.Name
	ticket.UpdatedAt = time.Now()
	if err := sr.Store.Ticket().Update(ctx, ticket); err != nil {
		return err
	}

	plain := "您的工单已转交由 " + newPrimary.Name + " 处理。"
	if !dr.IsMCP() {
		plain = "您的工单已转交由 " + mentionTag(newPrimary.UserID) + " 处理。"
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw: map[string]any{
			"ticket_id":     ticket.TicketID,
			"assignee_id":   newPrimary.UserID,
			"assignee_name": newPrimary.Name,
		},
		Markdown:  &runtime.MarkdownContent{Content: plain},
		PlainText: plain,
	})
}
