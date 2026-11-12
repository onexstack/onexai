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

// Package msg holds messaging commands.
package message

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	sendMsgLong = templates.LongDesc(`
        该命令用于【以 bot 身份向指定的人或群聊发送一条飞书消息】。

        【触发场景】：
        当需要给某人或某个群发一条消息时使用，如"帮我把这个通知发给 xx"、"给 xx 群发条消息"。

        【参数说明】：
        - <目标>（必需）：接收方标识，支持以下格式：
          - @对方：在本条消息里 @ 指定同事（推荐，最不容易发错人）
          - 用户名：如 toka.wu，自动转为 toka.wu@example.com 发送
          - 邮箱：如 toka.wu@example.com
          - user_id：如 u_toka（数字和字母组成的短串）
          - 群 chat_id：以 oc_ 开头
          - open_id：以 ou_ 开头
        - <消息内容>（必需）：要发送的正文，可含空格与换行。

        【限制】：
        - 仅 SRE 团队成员可用。
        - 消息一旦发出不可撤回，请确认目标无误再发送。`)

	sendMsgExample = templates.Examples(`
        # 发给本条消息里 @ 过的同事
        /send_msg @Toka Wu 请查收这份文档

        # 按用户名发送（自动转为邮箱）
        /send_msg toka.wu 你好

        # 按邮箱发送
        /send_msg toka.wu@example.com 你好

        # 发给群聊
        /send_msg oc_abc123 大家好`)
)

// sendMsgUsageHint is the usage hint returned when target resolution fails or an argument is missing.
const sendMsgUsageHint = `**send_msg 用法**

/send_msg <目标> <消息内容>

目标支持以下格式：

• @对方：在本条消息里 @ 指定同事（推荐）
• 用户名：如 toka.wu，自动转为邮箱
• 邮箱：如 toka.wu@example.com
• user_id：如 u_toka
• 群 chat_id：以 oc_ 开头
• open_id：以 ou_ 开头`

// userMentionRE matches Lark's @-mention placeholder "@_user_N"; the 1-based index selects from the mentions array.
var userMentionRE = regexp.MustCompile(`^@_user_(\d+)$`)

// SendMsgOptions defines the runtime options for the "send_msg" command.
type SendMsgOptions struct {
	out runtime.Responder
}

// NewSendMsgCmd creates the "send_msg" command: send a message to a given person or group chat.
func NewSendMsgCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &SendMsgOptions{out: out}

	cmd := &cobra.Command{
		Use:                   "send_msg <target> <content>",
		Short:                 "向指定的人或群聊发送一条飞书消息",
		Long:                  sendMsgLong,
		Example:               sendMsgExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ArbitraryArgs,
		Annotations: map[string]string{
			chat.SREOnly:   chat.True,
			chat.ChatGroup: chat.ChatGroupMessaging,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd.Context(), args); err != nil {
				return err
			}
			if err := o.Validate(cmd.Context(), args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), sr, runtime.From(cmd.Context()), args)
		},
	}
	return cmd
}

// Complete is a no-op for the send_msg command.
func (o *SendMsgOptions) Complete(ctx context.Context, args []string) error {
	return nil
}

// Validate ensures both a target and content are present.
func (o *SendMsgOptions) Validate(ctx context.Context, args []string) error {
	target, content := parseSendMsgArgs(args)
	if target == "" || content == "" {
		return o.out.SendMarkdownCard(ctx, "send_msg 用法", sendMsgUsageHint, "red")
	}
	return nil
}

// Run parses the arguments, resolves the target, and delivers the message.
func (o *SendMsgOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, args []string) error {
	if !dr.HasLarkContext() {
		return o.out.SendMarkdownCard(ctx, "send_msg 不可用", "当前环境未配置飞书客户端，无法发送消息", "red")
	}

	target, content := parseSendMsgArgs(args)
	recv, err := resolveSendMsgTarget(target, dr.Mentions)
	if err != nil {
		return o.out.SendMarkdownCard(ctx, "send_msg 用法", sendMsgUsageHint, "red")
	}

	if err := dr.LarkServices.SendText(ctx, recv, content); err != nil {
		return o.out.SendMarkdownCard(ctx, "发送失败", fmt.Sprintf("消息未能发出：%v", err), "red")
	}

	return o.out.SendText(ctx, fmt.Sprintf("✓ 已发送至 %s（%s）",
		target, describeReceiverType(recv.Type)))
}

// parseSendMsgArgs splits <target> from <content> at the first whitespace, keeping spaces and newlines in content.
func parseSendMsgArgs(args []string) (target, content string) {
	raw := strings.TrimSpace(strings.Join(args, " "))
	if raw == "" {
		return "", ""
	}
	idx := strings.IndexAny(raw, " \t\n")
	if idx == -1 {
		return raw, ""
	}
	target = raw[:idx]
	content = strings.TrimSpace(raw[idx+1:])
	return target, content
}

// resolveSendMsgTarget infers Lark's receive_id_type from the identifier's shape, covering group chat_id, open_id,
// @-mention placeholders, email, username, and user_id — six forms in all.
func resolveSendMsgTarget(target string, mentions []runtime.MentionedUser) (*v1.Receiver, error) {
	if target == "" {
		return nil, fmt.Errorf("target cannot be empty")
	}

	switch {
	case strings.HasPrefix(target, "oc_"):
		return &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: target}, nil
	case strings.HasPrefix(target, "ou_"):
		return &v1.Receiver{Type: v1.ReceiverTypeOpenID, Id: target}, nil
	}

	// @_user_N placeholder: the 1-based index selects the real open_id from the mentions array.
	if m := userMentionRE.FindStringSubmatch(target); m != nil {
		idx, _ := strconv.Atoi(m[1])
		if idx >= 1 && idx <= len(mentions) {
			return &v1.Receiver{Type: v1.ReceiverTypeOpenID, Id: mentions[idx-1].OpenID}, nil
		}
		return nil, fmt.Errorf("mention %q 超出本条消息的 @ 列表范围", target)
	}

	// Email: Lark's im API natively supports email as a receive_id_type.
	if strings.Contains(target, "@") {
		return &v1.Receiver{Type: "email", Id: target}, nil
	}

	// Any other single token is passed through as a user_id; existence is left for Lark to validate.
	return &v1.Receiver{Type: v1.ReceiverTypeUserID, Id: target}, nil
}

// describeReceiverType translates a receive_id_type into a Chinese description.
func describeReceiverType(typ string) string {
	switch typ {
	case v1.ReceiverTypeChatID:
		return "群聊"
	case v1.ReceiverTypeOpenID, v1.ReceiverTypeUserID:
		return "私聊"
	default:
		return "邮箱"
	}
}
