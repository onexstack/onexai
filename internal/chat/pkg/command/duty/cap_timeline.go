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

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

const timelineMaxSegments = 100

var timelineLong = `查看值班组的排班时间线。

默认给出「生效时间线」（已合并代班与不可用）；--layer 改为按图层展示，
用于确认某一层自己的轮转。`

func newTimelineCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	var (
		fromRaw string
		toRaw   string
		days    int
		byLayer bool
	)
	cmd := &cobra.Command{
		Use:           "timeline <group>",
		Short:         "查看值班时间线（生效 / 分层）",
		Long:          timelineLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		Annotations:   readAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimeline(cmd.Context(), sr, out, args, fromRaw, toRaw, days, byLayer)
		},
	}
	cmd.Flags().StringVar(&fromRaw, "from", "", "开始时间，RFC3339 或 2006-01-02[ 15:04]；默认现在")
	cmd.Flags().StringVar(&toRaw, "to", "", "结束时间；默认 --from + --days")
	cmd.Flags().IntVar(&days, "days", 7, "窗口天数（1-366），--from/--to 同时给出时忽略")
	cmd.Flags().BoolVar(&byLayer, "layer", false, "按图层展示，而不是只展示生效时间线")
	return cmd
}

func runTimeline(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder,
	args []string, fromRaw, toRaw string, days int, byLayer bool,
) error {
	group, err := requireGroupArg(args, "timeline")
	if err != nil {
		return err
	}
	loc, configured, err := groupLocation(ctx, sr, group)
	if err != nil {
		return dutyError(err)
	}
	if !configured {
		return runtime.NewUserError("值班组 [%s] 没有原生排班（可能仍在使用 Regen），无法查看时间线", group)
	}
	from, to, err := parseWindow(fromRaw, toRaw, days, loc, time.Now())
	if err != nil {
		return err
	}

	if byLayer {
		resp, err := sr.Duty.GetLayerTimelines(ctx, group, from, to)
		if err != nil {
			return dutyError(err)
		}
		return renderLayerTimeline(ctx, out, resp, loc)
	}
	resp, err := sr.Duty.GetTimeline(ctx, group, from, to)
	if err != nil {
		return dutyError(err)
	}
	return renderTimeline(ctx, out, resp, loc)
}

func renderTimeline(
	ctx context.Context, out runtime.Responder, resp *api.OncallTimelineResponse, loc *time.Location,
) error {
	segments := resp.Segments
	var sb strings.Builder
	fmt.Fprintf(&sb, "**%s** 时间线（%s ~ %s，时区 %s）\n\n",
		resp.Group, formatInstant(resp.From, loc), formatInstant(resp.To, loc), resp.Timezone)
	sb.WriteString("| 开始 | 结束 | 值班人 | 图层 | 代班 |\n|---|---|---|---|---|\n")
	for i, segment := range segments {
		if i >= timelineMaxSegments {
			fmt.Fprintf(&sb, "\n... 共 %d 段，仅显示前 %d 段\n", len(segments), timelineMaxSegments)
			break
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			formatInstant(segment.Start, loc), formatInstant(segment.End, loc),
			segment.UserName, blank(segment.LayerName), yesNo(segment.IsOverride)))
	}
	if len(segments) == 0 {
		sb.WriteString("\n该时间窗口内没有排班。\n")
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       resp,
		Markdown:  &runtime.MarkdownContent{Content: sb.String()},
		PlainText: sb.String(),
	})
}

func renderLayerTimeline(
	ctx context.Context, out runtime.Responder, resp *api.OncallLayerTimelinesResponse, loc *time.Location,
) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "**%s** 分层时间线（%s ~ %s，时区 %s）\n\n",
		resp.Group, formatInstant(resp.From, loc), formatInstant(resp.To, loc), resp.Timezone)
	sb.WriteString("**生效（含代班）**\n\n")
	sb.WriteString(segmentTable(resp.Effective, loc))
	for name, segments := range resp.Layers {
		fmt.Fprintf(&sb, "\n**图层 %s**\n\n", name)
		sb.WriteString(segmentTable(segments, loc))
	}
	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       resp,
		Markdown:  &runtime.MarkdownContent{Content: sb.String()},
		PlainText: sb.String(),
	})
}

func segmentTable(segments []api.OncallSegment, loc *time.Location) string {
	if len(segments) == 0 {
		return "（无）\n"
	}
	var sb strings.Builder
	sb.WriteString("| 开始 | 结束 | 值班人 | 代班 |\n|---|---|---|---|\n")
	for i, segment := range segments {
		if i >= timelineMaxSegments {
			fmt.Fprintf(&sb, "\n... 共 %d 段，仅显示前 %d 段\n", len(segments), timelineMaxSegments)
			break
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
			formatInstant(segment.Start, loc), formatInstant(segment.End, loc),
			segment.UserName, yesNo(segment.IsOverride)))
	}
	return sb.String()
}

func blank(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func yesNo(v bool) string {
	if v {
		return "是"
	}
	return "否"
}
