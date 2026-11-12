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
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	closeLong = templates.LongDesc(`
        该工具用于【一键关闭当前 Lark 群组所关联的工单】。
        当用户表达“问题已经解决了，可以关单”、“结束工单”、“帮忙把这个单子关一下”等意图时调用。

        【自动执行的后台操作】：
        - 将工单状态更新为“已关闭”。
        - 将群名状态改为 closed。
        （提示：工具会自动完成收尾动作，调用后无需再执行其他操作）

        【环境约束】：仅允许在工单群内调用；不接受任何参数。`)

	closeExample = templates.Examples(`
        # 确认问题处理完毕
        /close`)
)

// NewCloseCmd returns the /close command.
func NewCloseCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	return &cobra.Command{
		Use:                   "close",
		Short:                 "关闭当前工单（自动更新状态并同步群名，此命令不接受参数）",
		Long:                  closeLong,
		Example:               closeExample,
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

			if dr.SessionM != nil && dr.SessionM.IsClosed() {
				// Already closed — and the one thing still owed is the summary, when
				// it is missing. Closing does not depend on the model, so a summary
				// that failed (or a process that died before it ran) leaves a closed
				// ticket with no write-up and nothing in the group to say so. Saying
				// it again is the only retry this design has, and it costs nothing
				// when there is nothing to do.
				if ticket, err := activeTicket(ctx, sr, dr); err == nil && ticket != nil &&
					strings.TrimSpace(ticket.Summary) == "" {
					launchSummary(ctx, sr, dr, ticket)
					return out.SendText(ctx, "工单已经关闭；结案总结还没有生成，正在后台补生成，完成后会补发一条消息。")
				}
				return out.SendText(ctx, "工单已经关闭，无需执行此命令。")
			}

			ticket, err := updateTicketStatus(ctx, sr, dr, model.TicketStatusClosed)
			if err != nil {
				return err
			}
			if err := markSession(ctx, sr, dr, func(s *model.SessionM) {
				now := time.Now()
				s.ClosedAt = &now
			}); err != nil {
				return err
			}
			if err := syncChatName(ctx, dr, ticket); err != nil {
				return err
			}

			if err := out.SendResult(ctx, &runtime.CommandResult{
				Raw:       map[string]any{"ticket_id": ticket.TicketID, "status": "closed"},
				PlainText: "本次服务已结束，感谢您的反馈与配合。为避免不必要的打扰，本群将在 1 天后自动解散。",
			}); err != nil {
				return err
			}

			// After the reply, never before it: the close is done and the person is
			// waiting on nothing, while the summary is a model call that may take a
			// while or may fail. See launchSummary.
			launchSummary(ctx, sr, dr, ticket)
			return nil
		},
	}
}
