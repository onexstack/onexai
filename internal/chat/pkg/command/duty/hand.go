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
	"strings"
	"time"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	handLong = templates.LongDesc(`
        该工具用于【将当前工单及处理权移交（升级/转派）给其他专业的值班模块】。
        当用户表达“帮我转派给 xx 组”、“这个问题需要 xx 模块的人看一下”、“交接给 xx 负责人”时调用。

        【自动执行的后台操作】：
        - 将目标模块的主值班人（及备值班人）邀请进当前 Lark 群。
        - 将工单的归属组和处理人更新为该模块。
        - 在群内发送人员交接通知。
        （工具会自动完成上述动作，调用后无需再手动拉人或改单）

        【参数与防幻觉规则】：
        - 必须且只能提供 1 个 [module]（目标模块ID）。
        - 不确定模块 ID 时先用 /oncall --list 查询，不要猜测拼写。`)

	handExample = templates.Examples(`
        # 将工单转派给 bke 模块
        /hand bke`)
)

// NewHandCmd returns the /hand command.
func NewHandCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	return &cobra.Command{
		Use:                   "hand <module>",
		Short:                 "将当前工单转派给指定模块，并自动邀请该模块的值班人进群",
		Long:                  handLong,
		Example:               handExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.MinimumNArgs(1),
		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "【必填】目标值班模块的精确 ID（如 bke），每次只能传 1 个。",
			chat.ChatEnabled:                 chat.True,
			chat.ChatGroup:                   chat.ChatGroupDuty,
			chat.ChatOnly:                    chat.True,
			chat.SREOnly:                     chat.True,
			chat.MCPTool:                     chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHand(cmd.Context(), sr, out, runtime.From(cmd.Context()), args)
		},
	}
}

func runHand(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, dr *runtime.DynamicRuntime, args []string) error {
	if !dr.HasLarkContext() {
		return runtime.NewUserError("此命令只能在工单群中执行")
	}
	group, err := requireActiveGroup(ctx, sr, args[0])
	if err != nil {
		return err
	}

	ticket, err := activeTicket(ctx, sr, dr)
	if err != nil {
		return err
	}

	primary := group.Primary()
	inviteIDs := []string{primary.GetUserId()}
	backupWatchers := []*v1.Watcher{}
	if group.AssignStrategy() == v1.AssignStrategy_ASSIGN_PRIMARY_BACKUP {
		for i, w := range group.Watchers {
			if i == 0 {
				continue
			}
			inviteIDs = append(inviteIDs, w.GetUserId())
			backupWatchers = append(backupWatchers, w)
		}
	}

	if err := dr.LarkServices.InviteMembers(ctx, dr.Address(), "user_id", inviteIDs); err != nil {
		return err
	}

	ticket.GroupID = group.ID
	ticket.AssigneeID = primary.GetUserId()
	ticket.AssigneeName = primary.GetUserName()
	ticket.UpdatedAt = time.Now()
	if err := sr.Store.Ticket().Update(ctx, ticket); err != nil {
		return err
	}

	return out.SendResult(ctx, &runtime.CommandResult{
		Raw: map[string]any{
			"group_id":            group.ID,
			"primary_oncall_id":   primary.GetUserId(),
			"primary_oncall_name": primary.GetUserName(),
		},
		PlainText: handMessage(primary, backupWatchers),
	})
}

// handMessage renders the hand-over notice, @-mentioning the new on-call.
func handMessage(primary *v1.Watcher, backups []*v1.Watcher) string {
	if len(backups) == 0 {
		return "您的工单已交由 " + mentionTag(primary.GetUserId()) + " 处理。"
	}
	parts := make([]string, 0, len(backups))
	for _, b := range backups {
		parts = append(parts, mentionTag(b.GetUserId()))
	}
	return "您的工单已交由主值班人 " + mentionTag(primary.GetUserId()) +
		" 及备值班人 " + strings.Join(parts, " ") + "处理。"
}
