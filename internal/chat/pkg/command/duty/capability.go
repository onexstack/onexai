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
	"errors"
	"strings"
	"time"

	dutystore "github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// readAnnotations is the annotation set shared by the read-only duty
// subcommands: visible in chat, grouped with the duty commands, SRE-only, and
// exposed to MCP.
func readAnnotations() map[string]string {
	return map[string]string{
		chat.ChatEnabled: chat.True,
		chat.ChatGroup:   chat.ChatGroupDuty,
		chat.SREOnly:     chat.True,
		chat.MCPTool:     chat.True,
	}
}

// writeAnnotations is the annotation set shared by the mutating duty
// subcommands. They stay out of MCP on purpose: an agent must not create an
// override, mark someone unavailable, or page a group on its own.
func writeAnnotations() map[string]string {
	return map[string]string{
		chat.ChatEnabled: chat.True,
		chat.ChatGroup:   chat.ChatGroupDuty,
		chat.AdminOnly:   chat.True,
	}
}

// dutyError turns a service error into a user-facing one without losing the
// message the schedule layer produced (validation, not-found, ...).
func dutyError(err error) error {
	if errors.Is(err, dutystore.ErrScheduleUnavailable) {
		return runtime.NewUserError("该部署未启用原生排班服务")
	}
	return runtime.NewUserError("%v", err)
}

// requireGroupArg returns args[0] or a user error naming the argument.
func requireGroupArg(args []string, what string) (string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", runtime.NewUserError("请指定值班组，例如：/duty %s <值班组>", what)
	}
	return strings.TrimSpace(args[0]), nil
}

// groupLocation returns the schedule's timezone and whether the group has a
// native schedule. A legacy group falls back to UTC: there is no schedule to
// read a zone from, and an RFC 3339 argument carries its own offset anyway.
func groupLocation(
	ctx context.Context, sr *runtime.StaticRuntime, group string,
) (*time.Location, bool, error) {
	resp, err := sr.Duty.GetSchedule(ctx, group)
	if err != nil {
		return nil, false, err
	}
	if !resp.Configured || resp.Schedule == nil {
		return time.UTC, false, nil
	}
	loc := time.UTC
	if tz := strings.TrimSpace(resp.Schedule.Timezone); tz != "" {
		if loaded, err := time.LoadLocation(tz); err == nil {
			loc = loaded
		}
	}
	return loc, true, nil
}

// parseInstant accepts RFC 3339 and the date/time shapes people type in chat.
// The location is used for the shapes that carry no offset.
func parseInstant(raw string, loc *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, runtime.NewUserError("时间不能为空")
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if loc == nil {
		loc = time.UTC
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, runtime.NewUserError(
		"无法解析时间 %q，请使用 RFC3339（2006-01-02T15:04:05+08:00）或 2006-01-02[ 15:04]", raw)
}

// parseDate validates a YYYY-MM-DD day.
func parseDate(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if _, err := time.Parse("2006-01-02", raw); err != nil {
		return "", runtime.NewUserError("无法解析日期 %q，请使用 YYYY-MM-DD", raw)
	}
	return raw, nil
}

// parseWindow resolves a [from, to) window from explicit flags or --days.
func parseWindow(
	fromRaw, toRaw string, days int, loc *time.Location, now time.Time,
) (time.Time, time.Time, error) {
	if days == 0 {
		days = 7
	}
	if days < 1 || days > 366 {
		return time.Time{}, time.Time{}, runtime.NewUserError("--days 必须在 1-366 之间")
	}
	var (
		from time.Time
		to   time.Time
		err  error
	)
	if strings.TrimSpace(fromRaw) != "" {
		from, err = parseInstant(fromRaw, loc)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	} else {
		from = now.In(loc)
	}
	if strings.TrimSpace(toRaw) != "" {
		to, err = parseInstant(toRaw, loc)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	} else {
		to = from.Add(time.Duration(days) * 24 * time.Hour)
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, runtime.NewUserError("结束时间必须晚于开始时间")
	}
	if to.Sub(from) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, runtime.NewUserError("时间窗口不能超过 366 天")
	}
	return from, to, nil
}

// formatInstant renders an instant in the schedule's timezone.
func formatInstant(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "-"
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

// memberLabel renders one watcher with its rotation provenance.
func memberLabel(name, layer string, isOverride bool, until *time.Time, loc *time.Location) string {
	label := name
	if layer != "" {
		label += "（" + layer + "）"
	}
	if isOverride {
		label += " [代班]"
	}
	if until != nil {
		label += "，至 " + formatInstant(*until, loc)
	}
	return label
}
