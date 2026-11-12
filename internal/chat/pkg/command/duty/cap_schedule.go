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
	"time"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/validation"
)

var scheduleLong = `查看值班组的排班定义（时区、图层、轮转、参与者、代班/不可用/节假日数量）。

子命令 set / preview 用于修改或预览轮转定义：

  --layer "name=Primary,rotation=weekly,start=2026-11-02T00:00:00Z,shift=604800,users=alice@x|bob@x"

rotation 可取 daily/weekly/custom；shift 为班次秒数；users 用 | 分隔。`

func newScheduleCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "schedule <group>",
		Short:         "查看值班排班定义",
		Long:          scheduleLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   readAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScheduleShow(cmd.Context(), sr, out, args)
		},
	}
	cmd.AddCommand(newScheduleSetCmd(sr, out), newSchedulePreviewCmd(sr, out))
	return cmd
}

func runScheduleShow(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string,
) error {
	group, err := requireGroupArg(args, "schedule")
	if err != nil {
		return err
	}
	resp, err := sr.Duty.GetSchedule(ctx, group)
	if err != nil {
		return dutyError(err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "**%s** 排班\n\n", resp.Group)
	fmt.Fprintf(&sb, "- 来源: %s\n", resp.Source)
	if resp.LegacyScheduleID != "" {
		fmt.Fprintf(&sb, "- 旧 Regen schedule: %s\n", resp.LegacyScheduleID)
	}
	if !resp.Configured || resp.Schedule == nil {
		sb.WriteString("- 尚未配置原生排班\n")
		return out.SendResult(ctx, &runtime.CommandResult{
			Raw:       resp,
			Markdown:  &runtime.MarkdownContent{Content: sb.String()},
			PlainText: sb.String(),
		})
	}

	definition := resp.Schedule
	loc := time.UTC
	if definition.Timezone != "" {
		if loaded, err := time.LoadLocation(definition.Timezone); err == nil {
			loc = loaded
		}
	}
	fmt.Fprintf(&sb, "- 时区: %s\n", blank(definition.Timezone))
	fmt.Fprintf(&sb, "- 描述: %s\n", blank(definition.Description))
	fmt.Fprintf(&sb, "- 通知渠道: %s\n", blank(definition.NotificationChannel))
	fmt.Fprintf(&sb, "- 图层: %d，代班: %d，不可用: %d，节假日: %d\n",
		len(definition.Layers), len(definition.Overrides),
		len(definition.Unavailabilities), len(definition.Holidays))
	for _, layer := range definition.Layers {
		names := make([]string, 0, len(layer.Participants))
		for _, participant := range layer.Participants {
			names = append(names, participant.UserName)
		}
		fmt.Fprintf(&sb, "  - **%s**: %s，起点 %s，班次 %s，成员: %s\n",
			layer.Name, layer.RotationType,
			formatInstant(layer.RotationStart, loc),
			formatShift(layer.ShiftDurationSeconds),
			strings.Join(names, "、"))
	}

	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       resp,
		Markdown:  &runtime.MarkdownContent{Content: sb.String()},
		PlainText: sb.String(),
	})
}

func newScheduleSetCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	var (
		timezone string
		desc     string
		channel  string
		layers   []string
	)
	cmd := &cobra.Command{
		Use:           "set <group>",
		Short:         "保存轮转定义（管理员）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   writeAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScheduleSave(cmd.Context(), sr, out, args, timezone, desc, channel, layers, false, "", "", 0)
		},
	}
	cmd.Flags().StringVar(&timezone, "timezone", "", "排班时区；默认保留原值")
	cmd.Flags().StringVar(&desc, "description", "", "排班描述；默认保留原值")
	cmd.Flags().StringVar(&channel, "channel", "", "通知渠道；默认保留原值")
	cmd.Flags().StringArrayVar(&layers, "layer", nil, "图层定义，可重复；不传则保留现有图层")
	return cmd
}

func newSchedulePreviewCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	var (
		timezone string
		desc     string
		channel  string
		layers   []string
		fromRaw  string
		toRaw    string
		days     int
	)
	cmd := &cobra.Command{
		Use:           "preview <group>",
		Short:         "预览轮转定义（不落库，管理员）",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   writeAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScheduleSave(cmd.Context(), sr, out, args, timezone, desc, channel, layers, true, fromRaw, toRaw, days)
		},
	}
	cmd.Flags().StringVar(&timezone, "timezone", "", "排班时区；默认保留原值")
	cmd.Flags().StringVar(&desc, "description", "", "排班描述；默认保留原值")
	cmd.Flags().StringVar(&channel, "channel", "", "通知渠道；默认保留原值")
	cmd.Flags().StringArrayVar(&layers, "layer", nil, "图层定义，可重复；不传则保留现有图层")
	cmd.Flags().StringVar(&fromRaw, "from", "", "预览开始时间")
	cmd.Flags().StringVar(&toRaw, "to", "", "预览结束时间")
	cmd.Flags().IntVar(&days, "days", 7, "预览窗口天数（未给 --to 时使用）")
	return cmd
}

func runScheduleSave(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder,
	args []string, timezone, desc, channel string, layerSpecs []string,
	preview bool, fromRaw, toRaw string, days int,
) error {
	group, err := requireGroupArg(args, "schedule set")
	if err != nil {
		return err
	}
	existing, err := sr.Duty.GetSchedule(ctx, group)
	if err != nil {
		return dutyError(err)
	}

	var base *api.OncallSchedule
	if existing.Schedule != nil {
		base = existing.Schedule
	}
	loc := time.UTC
	if base != nil && strings.TrimSpace(base.Timezone) != "" {
		if loaded, err := time.LoadLocation(base.Timezone); err == nil {
			loc = loaded
		}
	}

	def := &api.OncallScheduleDefinition{
		Timezone:            firstNonEmpty(timezone, valueOr(base, func(s *api.OncallSchedule) string { return s.Timezone }), "UTC"),
		Description:         firstNonEmpty(desc, valueOr(base, func(s *api.OncallSchedule) string { return s.Description })),
		NotificationChannel: firstNonEmpty(channel, valueOr(base, func(s *api.OncallSchedule) string { return s.NotificationChannel })),
	}
	if len(layerSpecs) > 0 {
		def.Layers = make([]api.OncallScheduleLayer, 0, len(layerSpecs))
		for i, spec := range layerSpecs {
			layer, err := parseLayerSpec(spec, i, loc)
			if err != nil {
				return err
			}
			def.Layers = append(def.Layers, layer)
		}
	} else if base != nil {
		def.Layers = base.Layers
	}

	validator := validation.New(sr.Store)
	if preview {
		from, to, err := parseWindow(fromRaw, toRaw, days, loc, time.Now())
		if err != nil {
			return err
		}
		rq := &api.PreviewOncallScheduleRequest{
			Group: group, From: from, To: to, OncallScheduleDefinition: *def,
		}
		if err := validator.ValidatePreviewOncallScheduleRequest(ctx, rq); err != nil {
			return dutyError(err)
		}
		resp, err := sr.Duty.PreviewSchedule(ctx, group, def, from, to)
		if err != nil {
			return dutyError(err)
		}
		return renderTimeline(ctx, out, resp, loc)
	}

	rq := &api.SaveOncallScheduleRequest{Group: group, OncallScheduleDefinition: *def}
	if err := validator.ValidateSaveOncallScheduleRequest(ctx, rq); err != nil {
		return dutyError(err)
	}
	resp, err := sr.Duty.SaveSchedule(ctx, group, def)
	if err != nil {
		return dutyError(err)
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       resp,
		Markdown:  &runtime.MarkdownContent{Content: fmt.Sprintf("已保存 [%s] 的排班定义。", group)},
		PlainText: fmt.Sprintf("已保存 [%s] 的排班定义。", group),
	})
}

// parseLayerSpec parses one --layer value. The compact key=value form is
// deliberate: a JSON blob is unusable in Lark, and flags are what the commands
// already use.
func parseLayerSpec(spec string, index int, loc *time.Location) (api.OncallScheduleLayer, error) {
	values := make(map[string]string)
	for _, field := range strings.Split(spec, ",") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return api.OncallScheduleLayer{}, runtime.NewUserError(
				"图层定义 %q 格式错误，应为 key=value,key=value", spec)
		}
		values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}

	name := values["name"]
	if name == "" {
		name = fmt.Sprintf("Layer-%d", index+1)
	}
	rotation := strings.ToLower(values["rotation"])
	if rotation == "" {
		rotation = "weekly"
	}
	switch rotation {
	case "daily", "weekly", "custom":
	default:
		return api.OncallScheduleLayer{}, runtime.NewUserError(
			"图层 %s 的 rotation 必须是 daily/weekly/custom", name)
	}
	start, err := parseInstant(values["start"], loc)
	if err != nil {
		return api.OncallScheduleLayer{}, runtime.NewUserError("图层 %s 的 start 无效：%v", name, err)
	}
	shift, err := strconv.ParseInt(values["shift"], 10, 64)
	if err != nil || shift <= 0 {
		return api.OncallScheduleLayer{}, runtime.NewUserError(
			"图层 %s 的 shift 必须是正整数（秒）", name)
	}
	participants := make([]api.OncallScheduleParticipant, 0)
	for i, userName := range strings.Split(values["users"], "|") {
		userName = strings.TrimSpace(userName)
		if userName == "" {
			continue
		}
		participants = append(participants, api.OncallScheduleParticipant{
			UserName: userName, OrderIndex: int64(i),
		})
	}
	if len(participants) == 0 {
		return api.OncallScheduleLayer{}, runtime.NewUserError("图层 %s 至少需要一个 users 成员", name)
	}
	order := int64(index)
	if raw := values["order"]; raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			order = parsed
		}
	}
	return api.OncallScheduleLayer{
		Name:                 name,
		OrderIndex:           order,
		RotationType:         rotation,
		RotationStart:        start,
		ShiftDurationSeconds: shift,
		Participants:         participants,
	}, nil
}

func valueOr(schedule *api.OncallSchedule, get func(*api.OncallSchedule) string) string {
	if schedule == nil {
		return ""
	}
	return get(schedule)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func formatShift(seconds int64) string {
	if seconds <= 0 {
		return "-"
	}
	if seconds%(24*3600) == 0 {
		return fmt.Sprintf("%d 天", seconds/(24*3600))
	}
	if seconds%3600 == 0 {
		return fmt.Sprintf("%d 小时", seconds/3600)
	}
	return fmt.Sprintf("%d 秒", seconds)
}
