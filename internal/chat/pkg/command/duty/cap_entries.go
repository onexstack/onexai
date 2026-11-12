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
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/validation"
)

// callerIdentity returns the caller's platform identity, or "" when the
// command is running without one (a test or a REST caller that named no user).
func callerIdentity(ctx context.Context) string {
	if dr := runtime.From(ctx); dr != nil {
		return dr.Identity()
	}
	return ""
}

// ---------------------------------------------------------------- override

func newOverrideCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	parent := &cobra.Command{
		Use:           "override <group>",
		Short:         "查看代班（Override）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   readAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOverrideList(cmd.Context(), sr, out, args)
		},
	}

	var addUser, addStart, addEnd string
	add := &cobra.Command{
		Use:           "add <group>",
		Short:         "新增代班（管理员）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   writeAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOverrideAdd(cmd.Context(), sr, out, args, addUser, addStart, addEnd)
		},
	}
	add.Flags().StringVar(&addUser, "user", "", "代班人")
	add.Flags().StringVar(&addStart, "start", "", "开始时间，RFC3339 或 2006-01-02[ 15:04]")
	add.Flags().StringVar(&addEnd, "end", "", "结束时间")

	var removeID string
	rm := &cobra.Command{
		Use:           "rm <group>",
		Short:         "删除代班（管理员）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   writeAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOverrideDelete(cmd.Context(), sr, out, args, removeID)
		},
	}
	rm.Flags().StringVar(&removeID, "id", "", "代班 ID（可从列表获得）")

	parent.AddCommand(add, rm)
	return parent
}

func runOverrideList(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string,
) error {
	group, err := requireGroupArg(args, "override")
	if err != nil {
		return err
	}
	loc, _, _ := groupLocation(ctx, sr, group)
	resp, err := sr.Duty.ListOverrides(ctx, group)
	if err != nil {
		return dutyError(err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "**%s** 代班列表（%d）\n\n", group, len(resp.Overrides))
	if len(resp.Overrides) == 0 {
		sb.WriteString("（无）\n")
	} else {
		sb.WriteString("| ID | 代班人 | 开始 | 结束 | 创建人 |\n|---|---|---|---|---|\n")
		for _, item := range resp.Overrides {
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
				item.ID, item.OverrideUser,
				formatInstant(item.StartTime, loc), formatInstant(item.EndTime, loc),
				blank(item.CreatedBy)))
		}
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw: resp, Markdown: &runtime.MarkdownContent{Content: sb.String()}, PlainText: sb.String(),
	})
}

func runOverrideAdd(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder,
	args []string, user, startRaw, endRaw string,
) error {
	group, err := requireGroupArg(args, "override add")
	if err != nil {
		return err
	}
	if strings.TrimSpace(user) == "" {
		return runtime.NewUserError("请用 --user 指定代班人")
	}
	loc, _, _ := groupLocation(ctx, sr, group)
	start, err := parseInstant(startRaw, loc)
	if err != nil {
		return err
	}
	end, err := parseInstant(endRaw, loc)
	if err != nil {
		return err
	}
	rq := &api.CreateOncallOverrideRequest{
		Group: group, OverrideUser: user, StartTime: start, EndTime: end,
	}
	if err := validation.New(sr.Store).ValidateCreateOncallOverrideRequest(ctx, rq); err != nil {
		return dutyError(err)
	}
	created, err := sr.Duty.CreateOverride(ctx, group, user, start, end, callerIdentity(ctx))
	if err != nil {
		return dutyError(err)
	}
	message := fmt.Sprintf("已为 [%s] 新增代班 %s（%s ~ %s），ID: %s",
		group, created.OverrideUser,
		formatInstant(created.StartTime, loc), formatInstant(created.EndTime, loc), created.ID)
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       created,
		Markdown:  &runtime.MarkdownContent{Content: message},
		PlainText: message,
	})
}

func runOverrideDelete(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string, id string,
) error {
	group, err := requireGroupArg(args, "override rm")
	if err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return runtime.NewUserError("请用 --id 指定要删除的代班 ID")
	}
	if err := sr.Duty.DeleteOverride(ctx, group, id); err != nil {
		return dutyError(err)
	}
	message := fmt.Sprintf("已删除 [%s] 的代班 %s。", group, id)
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       map[string]any{"group": group, "deleted": id},
		Markdown:  &runtime.MarkdownContent{Content: message},
		PlainText: message,
	})
}

// ------------------------------------------------------------- unavailable

func newUnavailableCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	parent := &cobra.Command{
		Use:           "unavailable <group>",
		Short:         "查看不可用（Unavailability）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   readAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnavailableList(cmd.Context(), sr, out, args)
		},
	}

	var addUser, addStart, addEnd, addReason string
	add := &cobra.Command{
		Use:           "add <group>",
		Short:         "新增不可用（管理员）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   writeAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnavailableAdd(cmd.Context(), sr, out, args, addUser, addStart, addEnd, addReason)
		},
	}
	add.Flags().StringVar(&addUser, "user", "", "值班人")
	add.Flags().StringVar(&addStart, "start-date", "", "开始日期 YYYY-MM-DD")
	add.Flags().StringVar(&addEnd, "end-date", "", "结束日期 YYYY-MM-DD")
	add.Flags().StringVar(&addReason, "reason", "", "原因（可选）")

	var removeID string
	rm := &cobra.Command{
		Use:           "rm <group>",
		Short:         "删除不可用（管理员）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   writeAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnavailableDelete(cmd.Context(), sr, out, args, removeID)
		},
	}
	rm.Flags().StringVar(&removeID, "id", "", "不可用 ID")

	parent.AddCommand(add, rm)
	return parent
}

func runUnavailableList(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string,
) error {
	group, err := requireGroupArg(args, "unavailable")
	if err != nil {
		return err
	}
	resp, err := sr.Duty.ListUnavailabilities(ctx, group)
	if err != nil {
		return dutyError(err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "**%s** 不可用列表（%d）\n\n", group, len(resp.Unavailabilities))
	if len(resp.Unavailabilities) == 0 {
		sb.WriteString("（无）\n")
	} else {
		sb.WriteString("| ID | 值班人 | 开始 | 结束 | 原因 |\n|---|---|---|---|---|\n")
		for _, item := range resp.Unavailabilities {
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
				item.ID, item.UserName, item.StartDate, item.EndDate, blank(item.Reason)))
		}
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw: resp, Markdown: &runtime.MarkdownContent{Content: sb.String()}, PlainText: sb.String(),
	})
}

func runUnavailableAdd(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder,
	args []string, user, startDate, endDate, reason string,
) error {
	group, err := requireGroupArg(args, "unavailable add")
	if err != nil {
		return err
	}
	if strings.TrimSpace(user) == "" {
		return runtime.NewUserError("请用 --user 指定值班人")
	}
	startDate, err = parseDate(startDate)
	if err != nil {
		return err
	}
	endDate, err = parseDate(endDate)
	if err != nil {
		return err
	}
	rq := &api.CreateOncallUnavailabilityRequest{
		Group: group, UserName: user, StartDate: startDate, EndDate: endDate, Reason: reason,
	}
	if err := validation.New(sr.Store).ValidateCreateOncallUnavailabilityRequest(ctx, rq); err != nil {
		return dutyError(err)
	}
	created, err := sr.Duty.CreateUnavailability(ctx, group, user, startDate, endDate, reason, callerIdentity(ctx))
	if err != nil {
		return dutyError(err)
	}
	message := fmt.Sprintf("已为 [%s] 标记 %s 不可用（%s ~ %s），ID: %s",
		group, created.UserName, created.StartDate, created.EndDate, created.ID)
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       created,
		Markdown:  &runtime.MarkdownContent{Content: message},
		PlainText: message,
	})
}

func runUnavailableDelete(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string, id string,
) error {
	group, err := requireGroupArg(args, "unavailable rm")
	if err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return runtime.NewUserError("请用 --id 指定要删除的不可用记录")
	}
	if err := sr.Duty.DeleteUnavailability(ctx, group, id); err != nil {
		return dutyError(err)
	}
	message := fmt.Sprintf("已删除 [%s] 的不可用记录 %s。", group, id)
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       map[string]any{"group": group, "deleted": id},
		Markdown:  &runtime.MarkdownContent{Content: message},
		PlainText: message,
	})
}

// ----------------------------------------------------------------- holiday

func newHolidayCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	parent := &cobra.Command{
		Use:           "holiday <group>",
		Short:         "查看节假日",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   readAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHolidayList(cmd.Context(), sr, out, args)
		},
	}

	var addDate, addName, addCountry string
	add := &cobra.Command{
		Use:           "add <group>",
		Short:         "新增节假日（管理员）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   writeAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHolidayAdd(cmd.Context(), sr, out, args, addDate, addName, addCountry)
		},
	}
	add.Flags().StringVar(&addDate, "date", "", "日期 YYYY-MM-DD")
	add.Flags().StringVar(&addName, "name", "", "节假日名称")
	add.Flags().StringVar(&addCountry, "country", "", "国家代码（可选）")

	var removeID string
	rm := &cobra.Command{
		Use:           "rm <group>",
		Short:         "删除节假日（管理员）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   writeAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHolidayDelete(cmd.Context(), sr, out, args, removeID)
		},
	}
	rm.Flags().StringVar(&removeID, "id", "", "节假日 ID")

	parent.AddCommand(add, rm)
	return parent
}

func runHolidayList(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string,
) error {
	group, err := requireGroupArg(args, "holiday")
	if err != nil {
		return err
	}
	resp, err := sr.Duty.ListHolidays(ctx, group)
	if err != nil {
		return dutyError(err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "**%s** 节假日（%d）\n\n", group, len(resp.Holidays))
	if len(resp.Holidays) == 0 {
		sb.WriteString("（无）\n")
	} else {
		sb.WriteString("| ID | 日期 | 名称 | 国家 |\n|---|---|---|---|\n")
		for _, item := range resp.Holidays {
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
				item.ID, item.Date, item.Name, blank(item.CountryCode)))
		}
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw: resp, Markdown: &runtime.MarkdownContent{Content: sb.String()}, PlainText: sb.String(),
	})
}

func runHolidayAdd(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder,
	args []string, date, name, country string,
) error {
	group, err := requireGroupArg(args, "holiday add")
	if err != nil {
		return err
	}
	date, err = parseDate(date)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return runtime.NewUserError("请用 --name 指定节假日名称")
	}
	rq := &api.CreateOncallHolidayRequest{
		Group: group, Date: date, Name: name, CountryCode: country,
	}
	if err := validation.New(sr.Store).ValidateCreateOncallHolidayRequest(ctx, rq); err != nil {
		return dutyError(err)
	}
	created, err := sr.Duty.CreateHoliday(ctx, group, date, name, country)
	if err != nil {
		return dutyError(err)
	}
	message := fmt.Sprintf("已为 [%s] 添加节假日 %s（%s），ID: %s", group, created.Name, created.Date, created.ID)
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       created,
		Markdown:  &runtime.MarkdownContent{Content: message},
		PlainText: message,
	})
}

func runHolidayDelete(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string, id string,
) error {
	group, err := requireGroupArg(args, "holiday rm")
	if err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return runtime.NewUserError("请用 --id 指定要删除的节假日")
	}
	if err := sr.Duty.DeleteHoliday(ctx, group, id); err != nil {
		return dutyError(err)
	}
	message := fmt.Sprintf("已删除 [%s] 的节假日 %s。", group, id)
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       map[string]any{"group": group, "deleted": id},
		Markdown:  &runtime.MarkdownContent{Content: message},
		PlainText: message,
	})
}

// --------------------------------------------------------------- escalation

func newEscalationCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	parent := &cobra.Command{
		Use:           "escalation <group>",
		Short:         "查看升级策略",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   readAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEscalationShow(cmd.Context(), sr, out, args)
		},
	}

	var (
		enabled   bool
		auto      bool
		fromLevel int64
		tiers     []string
	)
	set := &cobra.Command{
		Use:           "set <group>",
		Short:         "保存升级策略（管理员）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   writeAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEscalationSet(cmd.Context(), sr, out, args, enabled, auto, fromLevel, tiers)
		},
	}
	set.Flags().BoolVar(&enabled, "enabled", false, "启用升级")
	set.Flags().BoolVar(&auto, "auto", false, "按工单级别自动升级")
	set.Flags().Int64Var(&fromLevel, "from-level", 0, "自动升级的最高工单级别（1-4）")
	set.Flags().StringArrayVar(&tiers, "tier", nil,
		"层级定义，可重复：timeout=300,target=schedule,group=bke 或 timeout=600,target=users,users=a|b")

	trigger := &cobra.Command{
		Use:           "trigger <group>",
		Short:         "手动升级工单（管理员）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   writeAnnotations(),
	}
	var ticketID string
	trigger.Flags().StringVar(&ticketID, "ticket", "", "工单 ID")
	trigger.RunE = func(cmd *cobra.Command, args []string) error {
		return runEscalationTrigger(cmd.Context(), sr, out, args, ticketID)
	}

	parent.AddCommand(set, trigger)
	return parent
}

func runEscalationShow(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string,
) error {
	group, err := requireGroupArg(args, "escalation")
	if err != nil {
		return err
	}
	resp, err := sr.Duty.GetEscalation(ctx, group)
	if err != nil {
		return dutyError(err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "**%s** 升级策略\n\n", group)
	if !resp.Configured || resp.Policy == nil {
		sb.WriteString("- 尚未配置原生排班，因此没有升级策略\n")
	} else {
		policy := resp.Policy
		fmt.Fprintf(&sb, "- 启用: %s，自动升级: %s，自动升级起始级别: P%d\n",
			yesNo(policy.Enabled), yesNo(policy.AutoEscalate), policy.AutoEscalateFromLevel)
		for i, tier := range policy.Tiers {
			fmt.Fprintf(&sb, "  %d. 超时 %d 秒，目标 %s，组: %s，人员: %s\n",
				i+1, tier.TimeoutSeconds, tier.TargetType,
				blank(tier.GroupName), blank(strings.Join(tier.UserNames, "、")))
		}
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw: resp, Markdown: &runtime.MarkdownContent{Content: sb.String()}, PlainText: sb.String(),
	})
}

func runEscalationSet(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder,
	args []string, enabled, auto bool, fromLevel int64, tierSpecs []string,
) error {
	group, err := requireGroupArg(args, "escalation set")
	if err != nil {
		return err
	}
	policy := &api.OncallEscalationPolicy{
		Enabled:               enabled,
		AutoEscalate:          auto,
		AutoEscalateFromLevel: fromLevel,
	}
	for _, spec := range tierSpecs {
		tier, err := parseTierSpec(spec)
		if err != nil {
			return err
		}
		policy.Tiers = append(policy.Tiers, tier)
	}
	rq := &api.SaveOncallEscalationRequest{Group: group, OncallEscalationPolicy: *policy}
	if err := validation.New(sr.Store).ValidateSaveOncallEscalationRequest(ctx, rq); err != nil {
		return dutyError(err)
	}
	resp, err := sr.Duty.SaveEscalation(ctx, group, policy)
	if err != nil {
		return dutyError(err)
	}
	message := fmt.Sprintf("已保存 [%s] 的升级策略。", group)
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       resp,
		Markdown:  &runtime.MarkdownContent{Content: message},
		PlainText: message,
	})
}

func runEscalationTrigger(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string, ticketID string,
) error {
	group, err := requireGroupArg(args, "escalation trigger")
	if err != nil {
		return err
	}
	if strings.TrimSpace(ticketID) == "" {
		return runtime.NewUserError("请用 --ticket 指定要升级的工单 ID")
	}
	resp, err := sr.Duty.Escalate(ctx, group, ticketID)
	if err != nil {
		return dutyError(err)
	}
	message := fmt.Sprintf("已升级工单 %s（值班组 %s，第 %d 层，状态 %s）。",
		resp.TicketID, resp.Group, resp.TierIndex, resp.Status)
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       resp,
		Markdown:  &runtime.MarkdownContent{Content: message},
		PlainText: message,
	})
}

func parseTierSpec(spec string) (api.OncallEscalationTier, error) {
	values := make(map[string]string)
	for _, field := range strings.Split(spec, ",") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return api.OncallEscalationTier{}, runtime.NewUserError(
				"层级定义 %q 格式错误，应为 key=value,key=value", spec)
		}
		values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	timeout, err := strconv.ParseInt(values["timeout"], 10, 64)
	if err != nil || timeout < 0 {
		return api.OncallEscalationTier{}, runtime.NewUserError("层级 timeout 必须是非负整数（秒）")
	}
	target := strings.ToLower(values["target"])
	if target == "" {
		target = "schedule"
	}
	switch target {
	case "schedule", "users", "both":
	default:
		return api.OncallEscalationTier{}, runtime.NewUserError("层级 target 必须是 schedule/users/both")
	}
	tier := api.OncallEscalationTier{
		TimeoutSeconds: timeout,
		TargetType:     target,
		GroupName:      values["group"],
	}
	for _, name := range strings.Split(values["users"], "|") {
		if name = strings.TrimSpace(name); name != "" {
			tier.UserNames = append(tier.UserNames, name)
		}
	}
	return tier, nil
}
