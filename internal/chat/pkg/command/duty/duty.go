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

	dutystore "github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	dutyLong = templates.LongDesc(`
        该工具用于查询值班模块当前的“值班人员名单（On-call / Duty Roster）”。
        当用户询问“今天谁值班”、“xxx 模块的负责人是谁”、“帮我找一下 xxx 的值班人”时调用。

        【重要规则】：
        - “值班组”与“值班模块”在概念上等价；可先用 /oncall --list 获取所有模块再判断。
        - 支持批量查询：可同时传入多个模块名。
        - 支持全量查询：不提供任何参数时返回所有模块的当前值班人。
        - 模块不存在与模块存在但暂无值班人，会给出不同提示；两者都可用 /oncall --list 对照。

        【子命令】：与控制台值班页面同源的原生排班能力。
        - /duty current [module...]  当前值班人、备值班、图层、是否代班、交接时间
        - /duty timeline <module>    排班时间线（--days/--from/--to，--layer 分层）
        - /duty schedule <module>    排班定义（时区、图层、参与者）
        - /duty override <module>    代班列表（add/rm 需管理员）
        - /duty unavailable <module> 不可用列表（add/rm 需管理员）
        - /duty holiday <module>     节假日列表（add/rm 需管理员）
        - /duty escalation <module>  升级策略（set/trigger 需管理员）
        模块名恰好与子命令同名时，用 /duty show <module> 查询。`)

	dutyExample = templates.Examples(`
        # 查询全部模块的当前值班人
        /duty

        # 查询单个模块
        /duty eks

        # 批量查询多个模块
        /duty eks monitor

        # 查看图层与交接时间
        /duty current eks

        # 查看未来 7 天的排班时间线
        /duty timeline eks --days 7

        # 模块名与子命令同名时
        /duty show timeline`)
)

// NewDutyCmd returns the /duty command.
//
//	/duty              list every module's current on-call
//	/duty <m1> <m2>    list the named modules' on-call
func NewDutyCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "duty [module...]",
		Short:                 "查询当前值班人员/值班组名单 (On-call Duty)",
		Long:                  dutyLong,
		Example:               dutyExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "可选参数。要查询的值班模块名称（如 eks、monitor），支持传入多个；不传则查询全部。",
			chat.ChatEnabled:                 chat.True,
			chat.ChatGroup:                   chat.ChatGroupDuty,
			chat.SREOnly:                     chat.True,
			chat.MCPTool:                     chat.True,
		},
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// "list" is the old sub-command spelling; keep it working.
			if len(args) == 1 && strings.EqualFold(args[0], "list") {
				args = nil
			}
			return runDuty(cmd.Context(), sr, out, args)
		},
	}

	show := &cobra.Command{
		Use:           "show [module...]",
		Short:         "查询当前值班人（显式写法，用于转义保留字）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		Annotations:   readAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDuty(cmd.Context(), sr, out, args)
		},
	}

	cmd.AddCommand(
		show,
		newCurrentCmd(sr, out),
		newTimelineCmd(sr, out),
		newScheduleCmd(sr, out),
		newOverrideCmd(sr, out),
		newUnavailableCmd(sr, out),
		newHolidayCmd(sr, out),
		newEscalationCmd(sr, out),
	)
	return cmd
}

// dutyRow is one row of the /duty card table.
type dutyRow struct {
	Module  string   `json:"module"`
	Primary []string `json:"primary"`
	Backup  []string `json:"backup"`
}

func runDuty(ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string) error {
	var (
		groups     []groupView
		unknown    []string
		noRotation []string
		loadErr    error
	)
	if len(args) == 0 {
		groups, loadErr = activeGroupViews(ctx, sr)
	} else {
		for _, arg := range args {
			name := arg
			if name == "" {
				continue
			}
			g, ok, err := sr.Duty.GetGroupInfo(ctx, name)
			if err != nil {
				loadErr = err
				continue
			}
			switch {
			case !ok:
				unknown = append(unknown, name)
			case len(g.Watchers) == 0:
				noRotation = append(noRotation, name)
			default:
				groups = append(groups, newGroupView(g))
			}
		}
	}

	// Nothing the caller named resolved: answer with the same taxonomy the
	// other duty commands use, rather than dropping the names silently.
	if len(groups) == 0 {
		if loadErr != nil {
			return out.SendText(ctx, groupServiceUnavailableMessage())
		}
		switch {
		case len(unknown) > 0:
			// An empty store cannot tell a wrong name from a service that never
			// loaded; say the latter rather than sending the user hunting for a
			// typo in a list that would also come back empty.
			known, err := sr.Duty.HasKnownGroups(ctx)
			if err != nil || !known {
				return out.SendText(ctx, groupServiceUnavailableMessage())
			}
			return runtime.NewUserError("%s", groupNotFoundMessage(strings.Join(unknown, ", ")))
		case len(noRotation) > 0:
			if rosterUnavailable(sr) {
				return out.SendText(ctx, groupServiceUnavailableMessage())
			}
			return out.SendText(ctx, groupWithoutRotationMessage(strings.Join(noRotation, ", ")))
		default:
			known, err := sr.Duty.HasKnownGroups(ctx)
			if err != nil || !known || rosterUnavailable(sr) {
				return out.SendText(ctx, groupServiceUnavailableMessage())
			}
			return out.SendText(ctx, "当前没有可用的值班组，请使用 /oncall --list 查看可用的值班组")
		}
	}

	var (
		sb           strings.Builder
		tableData    []dutyRow
		allOncallers []string
	)
	sb.WriteString("当前值班信息如下：\n\n")
	for _, g := range groups {
		tableData = append(tableData, g.row())

		mentions := make([]string, 0, len(g.watchers))
		for _, w := range g.watchers {
			name := w.GetUserName()
			if name == "" {
				name = w.GetUserId()
			}
			mentions = append(mentions, name)
			allOncallers = append(allOncallers, name)
		}
		if len(mentions) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("- **值班组**: %s\n", g.code))
		sb.WriteString(fmt.Sprintf("  - **值班人员**: %s\n", strings.Join(mentions, " ")))
	}

	// Partial matches still render, but the names that missed are reported
	// instead of vanishing.
	var notes []string
	if len(unknown) > 0 {
		notes = append(notes, fmt.Sprintf("未找到值班组：%s", strings.Join(unknown, "、")))
	}
	if len(noRotation) > 0 {
		notes = append(notes, fmt.Sprintf("以下值班组当前没有值班人：%s", strings.Join(noRotation, "、")))
	}
	plainText := "当前值班信息如下：\n" + strings.Join(allOncallers, ", ")
	if len(notes) > 0 {
		joined := strings.Join(notes, "\n")
		fmt.Fprintf(&sb, "\n%s\n", joined)
		plainText += "\n" + joined
	}

	// The card is rendered here rather than by a Feishu template, so its layout
	// — including how much air the title gets under the reply line above the
	// card — is decided in this repo. Markdown and PlainText stay the answer for
	// every surface without a card renderer (REST, MCP, TUI).
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:         tableData,
		Interactive: renderDutyCard(groups, notes, getThisWeekRange(), sr.DutyMaintainerUserID),
		Markdown:    &runtime.MarkdownContent{Content: sb.String()},
		PlainText:   plainText,
	})
}

// groupView is a duty group flattened for display: its code plus the
// primary/backup split the card and the markdown both need. Only the code names
// a group in /duty output — it is what every other /duty spelling takes as an
// argument, and the group's display name and description are deliberately left
// out of this listing.
type groupView struct {
	code     string
	watchers []*v1.Watcher
	primary  []string
	backup   []string
}

func activeGroupViews(ctx context.Context, sr *runtime.StaticRuntime) ([]groupView, error) {
	infos, err := sr.Duty.ActiveGroupInfos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]groupView, 0, len(infos))
	for _, g := range infos {
		out = append(out, newGroupView(g))
	}
	return out, nil
}

func newGroupView(g dutystore.Group) groupView {
	v := groupView{code: g.ID, watchers: g.Watchers}
	for i, w := range g.Watchers {
		name := w.GetUserName()
		if name == "" {
			name = w.GetUserId()
		}
		if i == 0 {
			v.primary = append(v.primary, name)
		} else {
			v.backup = append(v.backup, name)
		}
	}
	return v
}

func (v groupView) row() dutyRow {
	return dutyRow{Module: v.code, Primary: v.primary, Backup: v.backup}
}
