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
	"regexp"
	"strconv"
	"time"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	saveLong = templates.LongDesc(`
        该工具用于【保存当前工单群（防止被系统自动解散）】或【取消保存状态】。

        【触发场景】：
        - 保存：用户表示“这个群保留一下”、“别解散群”、“工单先留着观察”时。
        - 取消保存：用户表示“取消保存群”、“继续处理这个单子”时（加 --unsave）。

        【工具行为与约束】：
        - 默认将群状态置为“已保存”；带 --unsave 时退回“处理中”。
        - 不需要任何位置参数；重复调用不会报错。`)

	saveExample = templates.Examples(`
        # 保留当前工单群
        /save

        # 取消保存，回到处理中
        /save --unsave`)
)

// NewSaveCmd returns the /save command.
func NewSaveCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	var unsave bool
	cmd := &cobra.Command{
		Use:                   "save",
		Short:                 "保存当前工单群（防止自动解散），或取消保存",
		Long:                  saveLong,
		Example:               saveExample,
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
			if unsave {
				return runUnsave(cmd.Context(), sr, out, runtime.From(cmd.Context()))
			}
			return runSave(cmd.Context(), sr, out, runtime.From(cmd.Context()))
		},
	}
	cmd.Flags().BoolVar(&unsave, "unsave", false, "设为 true 时执行「取消保存」，将工单退回处理中。")
	return cmd
}

func runSave(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, dr *runtime.DynamicRuntime) error {
	if dr.SessionM != nil && dr.SessionM.IsSaved() {
		return out.SendText(ctx, "工单群已经处于保存状态，无需重复保存。")
	}
	ticket, err := updateTicketStatus(ctx, sr, dr, model.TicketStatusSaved)
	if err != nil {
		return err
	}
	if err := markSession(ctx, sr, dr, func(s *model.SessionM) {
		now := time.Now()
		s.SavedAt = &now
	}); err != nil {
		return err
	}
	if err := syncChatName(ctx, dr, ticket); err != nil {
		return err
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       map[string]any{"ticket_id": ticket.TicketID, "saved": true},
		PlainText: "工单群已保存。",
	})
}

func runUnsave(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, dr *runtime.DynamicRuntime) error {
	if dr.SessionM != nil && !dr.SessionM.IsSaved() {
		return out.SendText(ctx, "工单群当前未处于保存状态，无需取消。")
	}
	ticket, err := updateTicketStatus(ctx, sr, dr, model.TicketStatusProcessing)
	if err != nil {
		return err
	}
	if err := markSession(ctx, sr, dr, func(s *model.SessionM) {
		s.SavedAt = nil
	}); err != nil {
		return err
	}
	if err := syncChatName(ctx, dr, ticket); err != nil {
		return err
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       map[string]any{"ticket_id": ticket.TicketID, "saved": false},
		PlainText: "工单群已取消保存状态，重新激活工单处理流程。",
	})
}

var (
	levelLong = templates.LongDesc(`
        该工具用于设置或修改当前 Lark 群所绑定工单的严重级别（Level/Priority）。
        当用户意图为“提高/降低优先级”、“设置工单级别”、“改为 Px”时调用。

        【重要规则】：
        - 仅支持纯数字：1、2、3、4（1 最紧急）。
        - 无论用户说“P1”、“1级”还是“一级”，传入参数时都必须转换为纯数字。
        - 必须带上 [level] 位置参数。`)

	levelExample = templates.Examples(`
        # 把当前工单调到 P3
        /level 3

        # 升级为 1 级工单
        /level 1`)
)

// NewLevelCmd returns the /level command.
func NewLevelCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	return &cobra.Command{
		Use:                   "level <level>",
		Short:                 "修改/设置当前工单的优先级级别 (Ticket Level)",
		Long:                  levelLong,
		Example:               levelExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "必填参数。目标级别，仅允许填写数字 '1'、'2'、'3'、'4'。",
			chat.ChatEnabled:                 chat.True,
			chat.ChatGroup:                   chat.ChatGroupDuty,
			chat.ChatOnly:                    chat.True,
			chat.SREOnly:                     chat.True,
			chat.MCPTool:                     chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLevel(cmd.Context(), sr, out, runtime.From(cmd.Context()), args)
		},
	}
}

func runLevel(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, dr *runtime.DynamicRuntime, args []string) error {
	if !regexp.MustCompile(`^[1-4]$`).MatchString(args[0]) {
		return runtime.NewUserError("级别必须是 1、2、3 或 4")
	}
	if dr.SessionM != nil && dr.SessionM.IsClosed() {
		return out.SendText(ctx, runtime.ErrTicketClosed)
	}

	newLevel, _ := strconv.ParseInt(args[0], 10, 64)

	ticket, err := activeTicket(ctx, sr, dr)
	if err != nil {
		return err
	}
	oldLevel, groupID := ticket.Level, ticket.GroupID

	ticket.Level = newLevel
	ticket.UpdatedAt = time.Now()
	if err := sr.Store.Ticket().Update(ctx, ticket); err != nil {
		return fmt.Errorf("failed to update ticket level: %w", err)
	}
	if err := syncChatName(ctx, dr, ticket); err != nil {
		return err
	}

	// P4-defer escalation: a P4 ticket on a defer module was raised without
	// paging the on-call, so an upgrade to P1/P2/P3 has to pull them in now.
	// Best-effort — a failed invite must not undo the level change.
	escalated := false
	if dr.HasLarkContext() && oldLevel == 4 && newLevel < 4 && sr.IsP4DeferOncall(groupID) {
		if err := inviteP4EscalatedOncall(ctx, sr, dr, groupID); err != nil {
			slog.WarnContext(ctx, "[level] P4 escalation invite failed",
				"group_id", groupID, "ticket_id", ticket.TicketID, "error", err)
		} else {
			escalated = true
		}
	}

	plain := fmt.Sprintf("工单级别已设置为 [P%s]", args[0])
	if escalated {
		plain = fmt.Sprintf("工单级别已设置为 [P%s]，并已拉入 [%s] 值班同学进群处理", args[0], groupID)
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       map[string]any{"ticket_id": ticket.TicketID, "level": newLevel},
		PlainText: plain,
	})
}

// inviteP4EscalatedOncall pulls the module's current on-call into the chat after
// a deferred P4 ticket is escalated. It mirrors /hand's member selection:
// primary always, backups too under a primary_backup strategy.
func inviteP4EscalatedOncall(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, groupID string) error {
	group, _, err := sr.Duty.GetGroupInfo(ctx, groupID)
	if err != nil {
		return err
	}
	inviteIDs := []string{group.Primary().GetUserId()}
	if group.AssignStrategy() == v1.AssignStrategy_ASSIGN_PRIMARY_BACKUP {
		for i, w := range group.Watchers {
			if i == 0 {
				continue
			}
			inviteIDs = append(inviteIDs, w.GetUserId())
		}
	}
	return dr.LarkServices.InviteMembers(ctx, dr.Address(), "user_id", inviteIDs)
}
