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

package ai

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	sessionsLong = templates.LongDesc(`
        查看本机上有多少对话正在运行、各自绑定在哪个后端，以及本对话自己的状态。

        【触发场景】：
        - "现在有几个会话在跑"、"本机还有 claude 进程吗"
        - "我这个对话用的是哪个后端"

        【为什么只能在私聊里用】：
        这个命令报告的是**机器人所在主机**的运行情况，不是聊天内容。群里任何人
        都看得到的回答里不该出现这些；即便不列出其它会话的标识，主机上有几个会话
        本身也是不该由群成员知道的信息。

        【为什么不列出其它会话】：
        只报数量与分布，不报标识。会话标识就是别人的群/私聊 id，列出来等于把机器人
        服务过的会话名单交出去。要看自己的会话用 /new 或 /switch 的输出。`)

	sessionsExample = templates.Examples(`
        # 看看本机现在有多少会话、分别绑在哪些后端
        /sessions`)
)

// NewSessionsCmd creates the "sessions" command.
func NewSessionsCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &sessionsOptions{out: out}

	cmd := &cobra.Command{
		Use:                   "sessions",
		Aliases:               []string{"session"},
		Short:                 "查看当前运行的会话概况（仅管理员、仅私聊）",
		Long:                  sessionsLong,
		Example:               sessionsExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Annotations: map[string]string{
			chat.ChatGroup: chat.ChatGroupAI,
			chat.AdminOnly: chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.Run(cmd.Context(), sr, runtime.From(cmd.Context()))
		},
	}

	return cmd
}

// sessionsOptions is what /sessions needs: somewhere to answer.
type sessionsOptions struct {
	out runtime.Responder
}

// Run reports the live conversations without naming them.
func (o *sessionsOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime) error {
	if sr == nil || sr.Sessions == nil {
		return sr.UserError("本服务未启用会话管理")
	}
	// A group chat is refused rather than redacted. The count of processes the bot
	// has spawned, and which backends they run on, describes the host it runs on;
	// that is not something a group member should be able to read out, however
	// few identifiers the answer carries.
	if dr == nil || dr.Receiver == nil || !dr.IsP2P() {
		return sr.UserError("该命令只能在私聊中使用：它报告的是机器人所在主机的运行情况")
	}

	live := sr.Sessions.Sessions()
	counts := map[string]int{}
	var pending, busy int
	for _, sess := range live {
		counts[sess.State().Backend]++
		if sess.Busy() {
			busy++
		}
		if sess.PendingPermission() != nil {
			pending++
		}
	}

	var b strings.Builder
	b.WriteString(sr.Translate("本机当前有 **%d** 个会话在运行，其中 **%d** 个正在处理消息。\n", len(live), busy))
	// Every backend present is listed, not just the two known today: a backend
	// whose conversations were left out would make this report disagree with its
	// own total.
	for _, backend := range sortedKeys(counts) {
		b.WriteString(sr.Translate("- %s 后端：%d 个\n", backend, counts[backend]))
	}
	if pending > 0 {
		b.WriteString(sr.Translate("有 %d 个会话正在等待审批。\n", pending))
	}

	b.WriteString("\n")
	b.WriteString(o.own(sr, dr))
	return o.out.SendResult(ctx, &runtime.CommandResult{PlainText: b.String()})
}

// own describes the caller's own conversation, which is the only one whose
// details the answer may contain.
func (o *sessionsOptions) own(sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime) string {
	key := conversationKey(dr)
	if key == "" {
		return sr.Translate("本对话没有可识别的会话标识。")
	}

	sess, ok := sr.Sessions.Lookup(key)
	if !ok {
		return sr.Translate("本对话还没有开始：发送一条消息或使用 /new 即可新建。")
	}

	state := sess.State()
	var b strings.Builder
	b.WriteString(sr.Translate("本对话：后端 **%s**", state.Backend))
	if state.Backend == agent.BackendCLI {
		if state.BackendSessionID != "" {
			b.WriteString(sr.Translate("，已建立本机会话"))
		} else {
			b.WriteString(sr.Translate("，尚未建立本机会话（下一条消息开始时建立）"))
		}
	}
	switch {
	case sess.Busy():
		b.WriteString(sr.Translate("，正在处理消息"))
	case sess.PendingPermission() != nil:
		b.WriteString(sr.Translate("，正在等待审批"))
	}
	if idle := sess.IdleFor(); idle > 0 {
		b.WriteString(sr.Translate("，空闲 %s", roundDuration(idle)))
	}
	b.WriteString(sr.Translate("。"))
	return b.String()
}

// roundDuration renders an age at the granularity a human reads it in: seconds
// under a minute, whole minutes under an hour, whole hours beyond. A raw
// time.Duration here would print nanoseconds nobody asked for.
func roundDuration(d time.Duration) time.Duration {
	switch {
	case d < time.Minute:
		return d.Round(time.Second)
	case d < time.Hour:
		return d.Round(time.Minute)
	default:
		return d.Round(time.Hour)
	}
}

// sortedKeys returns the map's keys in a fixed order, so two runs over the same
// state read the same way.
func sortedKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
