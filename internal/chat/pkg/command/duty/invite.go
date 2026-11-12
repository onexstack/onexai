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
	"strings"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	inviteLong = templates.LongDesc(`
        该工具用于【将指定值班模块（Module）的当前值班人员邀请到当前 Lark 工单群内协助处理问题】。

        【触发场景】：用户表达“把 xxx 模块的人拉进来”、“邀请 xxx 负责人进群看下”、“帮我拉一下 xxx 的值班”时。

        【参数规则与防错】：
        - 输入【值班模块ID】（如 bke、monitor、eks），不能输入具体人名。
        - 严格要求且仅接受 1 个模块参数；要拉多个模块请分多次调用。
        - 直接 @ 某人时可不带模块参数，会把被 @ 的人拉进群。

        【环境】：在工单群内拉入对应值班人；在私聊中触发时会自动复用 /oncall 建单。`)

	inviteExample = templates.Examples(`
        # 把 bke 的值班同学拉进群
        /invite bke

        # 直接 @ 某人进群
        /invite @张三`)
)

// NewInviteCmd returns the /invite command.
func NewInviteCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	return &cobra.Command{
		Use:                   "invite <module>",
		Short:                 "邀请指定模块的值班人员进入当前群组",
		Long:                  inviteLong,
		Example:               inviteExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		// Not MinimumNArgs(1): the direct @-mention mode supplies no positional
		// argument at all, so a cobra-level minimum would reject it outright.
		Args: cobra.ArbitraryArgs,
		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "要邀请的【值班模块ID】（不是人名），每次只能填一个；直接 @ 人时可不填。",
			chat.ChatEnabled:                 chat.True,
			chat.ChatGroup:                   chat.ChatGroupDuty,
			chat.SREOnly:                     chat.True,
			chat.MCPTool:                     chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInvite(cmd.Context(), sr, out, runtime.From(cmd.Context()), args)
		},
	}
}

func runInvite(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, dr *runtime.DynamicRuntime, args []string) error {
	if !dr.HasLarkContext() {
		return runtime.NewUserError("此命令只能在 Lark 会话中执行")
	}

	// Direct @-mention mode: invite exactly who was named, no module lookup.
	userIDs, openIDs := splitMentionIDs(dr.Mentions)
	if len(userIDs) > 0 || len(openIDs) > 0 {
		if len(userIDs) > 0 {
			if err := dr.LarkServices.InviteMembers(ctx, dr.Address(), "user_id", userIDs); err != nil {
				return err
			}
		}
		if len(openIDs) > 0 {
			if err := dr.LarkServices.InviteMembers(ctx, dr.Address(), "open_id", openIDs); err != nil {
				return err
			}
		}
		var sb strings.Builder
		for _, id := range userIDs {
			fmt.Fprintf(&sb, "%s ", mentionTag(id))
		}
		for _, m := range dr.Mentions {
			if m.UserID == "" && m.OpenID == "" && m.Name != "" {
				fmt.Fprintf(&sb, "%s ", m.Name)
			}
		}
		return out.SendResult(ctx, &runtime.CommandResult{
			Raw:       map[string]any{"chat_id": dr.Address(), "invited_user_ids": userIDs, "invited_open_ids": openIDs},
			PlainText: fmt.Sprintf("已邀请 %s进群", sb.String()),
		})
	}

	if len(args) == 0 {
		return runtime.ErrGroupRequired
	}
	target := strings.ToLower(args[0])

	// In a 1:1 chat there is no group to invite into; raise the ticket instead.
	if dr.IsP2P() {
		return createTicketForModule(ctx, sr, dr, out, target, target+"问题处理", 4)
	}

	group, err := requireActiveGroup(ctx, sr, args[0])
	if err != nil {
		return err
	}

	primary := group.Primary()
	inviteIDs := []string{primary.GetUserId()}
	backupIDs := []string{}
	if group.AssignStrategy() == v1.AssignStrategy_ASSIGN_PRIMARY_BACKUP {
		for i, w := range group.Watchers {
			if i == 0 {
				continue
			}
			inviteIDs = append(inviteIDs, w.GetUserId())
			backupIDs = append(backupIDs, w.GetUserId())
		}
	}
	if err := dr.LarkServices.InviteMembers(ctx, dr.Address(), "user_id", inviteIDs); err != nil {
		return err
	}

	message := fmt.Sprintf("已邀请 [%s] 值班人 %s 进群协助", group.ID, mentionTag(primary.GetUserId()))
	if len(backupIDs) > 0 {
		parts := make([]string, 0, len(backupIDs))
		for _, id := range backupIDs {
			parts = append(parts, mentionTag(id))
		}
		message = fmt.Sprintf("已邀请 [%s] 主值班人 %s 及备值班人 %s进群协助",
			group.ID, mentionTag(primary.GetUserId()), strings.Join(parts, " "))
	}

	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       map[string]any{"chat_id": dr.Address(), "invited_user_ids": inviteIDs},
		Markdown:  &runtime.MarkdownContent{Content: message},
		PlainText: fmt.Sprintf("已邀请 %s 值班人进群协助", group.ID),
	})
}

// splitMentionIDs separates the mentioned users into the two id namespaces Lark
// invites by, skipping bots (their mentions carry no user_id here).
func splitMentionIDs(mentions []runtime.MentionedUser) (userIDs, openIDs []string) {
	for _, m := range mentions {
		if m.IsBot {
			continue
		}
		switch {
		case m.UserID != "":
			userIDs = append(userIDs, m.UserID)
		case m.OpenID != "":
			openIDs = append(openIDs, m.OpenID)
		}
	}
	return userIDs, openIDs
}
