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
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	openLong = templates.LongDesc(`
        该工具用于【重新打开当前所在的 Lark 群组（工单群）绑定的工单】。
        当用户表达“帮我重新打开这个工单”、“问题还没解决，重开一下单子”时调用。

        【工具行为与约束】：
        - 行为：将工单状态从“已关闭”重置为“处理中”，并把群名状态恢复为处理中。
        - 无参调用：纯状态切换，不接受任何参数。`)

	openExample = templates.Examples(`
        # 重新打开已关闭的工单
        /open`)
)

// NewOpenCmd returns the /open command.
func NewOpenCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	return &cobra.Command{
		Use:                   "open",
		Short:                 "重新打开当前的已关闭工单（恢复为处理中状态）",
		Long:                  openLong,
		Example:               openExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Annotations: map[string]string{
			chat.ChatEnabled: chat.True,
			chat.ChatGroup:   chat.ChatGroupDuty,
			chat.ChatOnly:    chat.True,
			chat.SREOnly:     chat.True,
			chat.MCPTool:     chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dr := runtime.From(ctx)

			if dr.SessionM == nil {
				return out.SendText(ctx, "无法获取聊天上下文")
			}
			if dr.SessionM.AssociatedTicketID == "" {
				return out.SendText(ctx, "未找到此会话的工单号")
			}

			ticket, err := updateTicketStatus(ctx, sr, dr, model.TicketStatusProcessing)
			if err != nil {
				return err
			}
			if err := markSession(ctx, sr, dr, func(s *model.SessionM) {
				s.ClosedAt = nil
			}); err != nil {
				return err
			}
			if err := syncChatName(ctx, dr, ticket); err != nil {
				return err
			}

			return out.SendResult(ctx, &runtime.CommandResult{
				Raw:       map[string]any{"ticket_id": ticket.TicketID, "status": "reopened"},
				PlainText: "工单已被重新打开，请值班人继续处理工单",
			})
		},
	}
}
