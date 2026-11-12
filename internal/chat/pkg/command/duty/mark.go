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
	"time"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	markLong = templates.LongDesc(`
        该工具用于为【当前 Lark 群组(工单群)】添加处理备注，完成对该工单的标记。
        当用户意图包含“标记一下这个单子”、“备注一下处理过程”、“这个要复盘”、“把这个单子转需求”时调用。

        【工具行为与参数规则】：
        - [描述] 参数：记录本次标记的具体内容，支持长文本。
        - 默认行为（无Flag）：只把描述写入工单的“处理记录”，不做状态分类。
        - 复盘标记（--review/-r）：用户明确要求“需要复盘”时携带。
        - 需求标记（--demand/-d）：用户明确要求“转需求”时携带。
        - 两个Flag互不排斥；用户没有明确表达时不要自行添加。

        【环境限制】：仅在工单群内有效，依赖群组关联的工单信息。`)

	markExample = templates.Examples(`
        # 只记一笔备注
        /mark 重启了网关pod

        # 标记需要复盘
        /mark --review 需要补充SOP手册

        # 标记转需求
        /mark --demand 用户想要一个导出功能`)
)

// MarkOptions defines configuration for the "mark" command.
type MarkOptions struct {
	out runtime.Responder

	// Review sets the ticket's "needs a retrospective" mark; Demand sets its "product demand" mark.
	// The two are not mutually exclusive; when neither is given, only a handling note is written and no status classification is made.
	Review bool
	Demand bool
}

// NewMarkCmd creates the "mark" command.
func NewMarkCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &MarkOptions{out: out}

	cmd := &cobra.Command{
		Use:                   "mark <description>...",
		Short:                 "为当前工单添加处理备注并完成标记",
		Long:                  markLong,
		Example:               markExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.MinimumNArgs(1),
		Annotations: map[string]string{
			chat.ChatOnly:                    chat.True,
			cobrax.AnnotationArgPrefix + "0": "必需参数。本次标记的描述信息（处理备注、复盘要点或需求详情），支持含空格的长文本。",
			chat.ChatEnabled:                 chat.True,
			chat.ChatGroup:                   chat.ChatGroupDuty,
			chat.MCPTool:                     chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			dr := runtime.From(cmd.Context())
			if err := o.Validate(dr, args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), sr, dr, args)
		},
	}

	cmd.Flags().BoolVarP(&o.Review, "review", "r", false, "将工单标记为「需要复盘」")
	cmd.Flags().BoolVarP(&o.Demand, "demand", "d", false, "将工单标记为「需求」")

	return cmd
}

// Validate ensures the current conversation has an associated ticket and that a
// description was given.
func (o *MarkOptions) Validate(dr *runtime.DynamicRuntime, args []string) error {
	if dr == nil || dr.SessionM == nil || dr.SessionM.AssociatedTicketID == "" {
		return runtime.NewUserError("当前群未关联工单，无法标记")
	}
	if joinDescription(args) == "" {
		return runtime.NewUserError("请填写标记的描述信息，例如：/mark 需要补充 SOP 手册")
	}
	return nil
}

// joinDescription joins a description written as space-separated words back into one sentence.
func joinDescription(args []string) string {
	return strings.TrimSpace(strings.Join(args, " "))
}

// Run executes the mark operation against the local ticket record.
//
// The mark is appended as a note rather than written onto the ticket: a ticket
// carries one row of state, and a handling log is a list of events. Keeping
// them apart is what gives every entry an author and a time, and lets a second
// /mark add to the log instead of overwriting the first.
//
// The ticket row itself is left untouched, so its updated_at does not advance:
// nothing about the ticket changed. The mark's own time is the note's
// created_at.
func (o *MarkOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, args []string) error {
	ticket, err := activeTicket(ctx, sr, dr)
	if err != nil {
		return err
	}

	note := &model.TicketNoteM{
		TicketID:   ticket.TicketID,
		AuthorID:   callerID(dr),
		AuthorName: callerName(ctx, dr),
		Note:       joinDescription(args),
		Review:     o.Review,
		Demand:     o.Demand,
		CreatedAt:  time.Now(),
	}
	if err := sr.Store.TicketNote().Create(ctx, note); err != nil {
		return fmt.Errorf("更新工单失败: %w", err)
	}

	actions := make([]string, 0, 2)
	if o.Review {
		actions = append(actions, "需要复盘")
	}
	if o.Demand {
		actions = append(actions, "需要转需求")
	}

	actionType := "处理备注"
	if len(actions) > 0 {
		actionType = strings.Join(actions, "、")
	}

	return o.out.SendResult(ctx, &runtime.CommandResult{
		Raw:       map[string]any{"ticket_id": ticket.TicketID, "action_type": actionType},
		Markdown:  &runtime.MarkdownContent{Content: "工单已完成标记。"},
		PlainText: "工单已完成标记。",
	})
}
