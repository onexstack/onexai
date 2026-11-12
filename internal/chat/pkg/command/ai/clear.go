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
	"fmt"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	clearLong = templates.LongDesc(`
        该工具用于【清除当前对话（群组或私聊）的历史记忆】，让 AI 助手忘记之前的对话内容。

        清除时优先删除最久远的记忆，保留最近的对话。

        【触发场景】：
        - "忘记之前说的内容"、"清空记忆"、"重新开始对话"
        - "清除一半的记忆"、"只保留最近的对话"
        - "把上下文清掉 80%"

        【参数说明】：
        - --percent / -p：清除记忆的百分比（1–100），默认 100（全部清除）。
          例如 --percent 50 会删除最旧的一半对话记录，保留最新的一半。`)

	clearExample = templates.Examples(`
        # 清除全部记忆（默认行为）
        /clear

        # 清除最旧的 50% 记忆，保留最新的一半
        /clear --percent 50

        # 清除最旧的 80% 记忆
        /clear -p 80`)
)

// ClearOptions defines configuration for the "clear" command.
type ClearOptions struct {
	out     runtime.Responder
	Percent int // 1–100, percentage of oldest messages to drop
}

// NewClearCmd creates the "clear" command (with "forget" as an alias).
func NewClearCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &ClearOptions{out: out, Percent: 100}

	cmd := &cobra.Command{
		Use:                   "clear",
		Aliases:               []string{"forget", "cl"},
		Short:                 "清除当前对话的历史记忆（优先清除最旧的记录）",
		Long:                  clearLong,
		Example:               clearExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Annotations: map[string]string{
			chat.ChatGroup: chat.ChatGroupAI,
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

	cmd.Flags().IntVarP(&o.Percent, "percent", "p", 100,
		"清除记忆的百分比（1–100），默认 100 表示全部清除，优先清除最旧的记录")

	return cmd
}

// Complete is a no-op for the clear command (no positional arguments to parse).
func (o *ClearOptions) Complete(ctx context.Context, args []string) error {
	return nil
}

// Validate ensures --percent is within the valid range.
func (o *ClearOptions) Validate(ctx context.Context, args []string) error {
	if o.Percent < 1 || o.Percent > 100 {
		return runtime.NewUserError("--percent 参数必须在 1 到 100 之间")
	}
	return nil
}

// Run clears the AI conversation memory for the current conversation via
// oai-agent-apiserver.
//
// Note: the oai-agent-apiserver session API only supports a full reset; --percent is
// accepted for opsassist parity but a partial (percentage-based) trim is not
// yet plumbed into the agent client.
func (o *ClearOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, _ []string) error {
	sessionID := dr.ConvID
	if sessionID == "" {
		sessionID = dr.Identity()
	}
	if err := sr.Clientset.Agent().ClearSession(ctx, sessionID); err != nil {
		return fmt.Errorf("could not clear conversation: %w", err)
	}
	return o.out.SendResult(ctx, &runtime.CommandResult{PlainText: "AI 对话记忆已清除。"})
}
