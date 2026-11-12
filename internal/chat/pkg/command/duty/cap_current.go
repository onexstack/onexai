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

var currentLong = `查询指定（或不指定）值班组的当前值班人。

与 /duty 的区别：/duty 只列姓名；本命令同时给出图层、是否代班、本轮值班的结束时间。
不传值班组时查询所有组。`

func newCurrentCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	var atRaw string
	cmd := &cobra.Command{
		Use:           "current [group...]",
		Short:         "查询当前值班人、备值班与交接时间",
		Long:          currentLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		Annotations:   readAnnotations(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCurrent(cmd.Context(), sr, out, args, atRaw)
		},
	}
	cmd.Flags().StringVar(&atRaw, "at", "", "查询指定时刻，RFC3339 或 2006-01-02[ 15:04]；默认现在")
	return cmd
}

func runCurrent(
	ctx context.Context, sr *runtime.StaticRuntime, out runtime.Responder, args []string, atRaw string,
) error {
	// The schedule's timezone is only needed to interpret a local-looking --at
	// and to render the shift end; a bare "now" query needs neither.
	loc := time.UTC
	if len(args) == 1 {
		if loaded, _, err := groupLocation(ctx, sr, args[0]); err == nil {
			loc = loaded
		}
	}
	var at time.Time
	if strings.TrimSpace(atRaw) != "" {
		parsed, err := parseInstant(atRaw, loc)
		if err != nil {
			return err
		}
		at = parsed
	}

	resp, err := sr.Duty.ListCurrent(ctx, args, at)
	if err != nil {
		return dutyError(err)
	}
	if len(resp.Items) == 0 {
		return out.SendText(ctx, "没有找到匹配的值班组")
	}

	var sb strings.Builder
	plain := make([]string, 0, len(resp.Items))
	for _, item := range resp.Items {
		if item.Current == nil {
			fmt.Fprintf(&sb, "- **%s**: 当前无值班人\n", item.Group)
			plain = append(plain, item.Group+": 当前无值班人")
			continue
		}
		fmt.Fprintf(&sb, "- **%s**: %s\n", item.Group, memberLabelOf(item.Current, loc))
		line := item.Group + ": " + item.Current.UserName
		if len(item.Backups) > 0 {
			names := make([]string, 0, len(item.Backups))
			for _, backup := range item.Backups {
				names = append(names, backup.UserName)
			}
			fmt.Fprintf(&sb, "  - 备值班: %s\n", strings.Join(names, "、"))
			line += "；备值班: " + strings.Join(names, "、")
		}
		plain = append(plain, line)
	}

	return out.SendResult(ctx, &runtime.CommandResult{
		Raw:       resp,
		Markdown:  &runtime.MarkdownContent{Content: sb.String()},
		PlainText: "当前值班信息如下：\n" + strings.Join(plain, "\n"),
	})
}

func memberLabelOf(m *api.OncallCurrentMember, loc *time.Location) string {
	if m == nil {
		return "-"
	}
	return memberLabel(m.UserName, m.LayerName, m.IsOverride, m.Until, loc)
}
