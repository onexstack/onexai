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

package cronjob

import (
	"context"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/command/cmdutil"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

type cronJobDeleteOptions struct {
	out       runtime.Responder
	CronJobID string
}

func newCronJobDeleteCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &cronJobDeleteOptions{out: out}

	cmd := &cobra.Command{
		Use:   "delete <cronjob_id>",
		Short: "删除单条 CronJob 并停止其后续调度：CRONJOB_ID 为位置参数",
		Long: templates.LongDesc(`
			用途：删除一个 CronJob，删除后该定时任务不再触发。CRONJOB_ID 用位置参数传入。
			仅能删除调用者本人拥有的 CronJob，否则报"未找到"。`),
		Example: templates.Examples(`
			# 删除指定 CronJob（停止其后续调度）
			cronjob delete cronjob-tyw0g4`),
		Args:                  cobra.ExactArgs(1),
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		SilenceErrors:         true,
		Annotations: map[string]string{
			// See the note in get.go: the <cronjob_id> token in Use is what
			// makes this argument visible to cobrax.
			cobrax.AnnotationArgPrefix + "0": "要删除的 CronJob ID，如 cronjob-tyw0g4。",
			chat.ChatEnabled:                 chat.True,
			chat.ChatGroup:                   chat.ChatGroupCronJob,
			chat.SREOnly:                     chat.True,
			chat.MCPTool:                     chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd.Context(), args); err != nil {
				return err
			}
			if err := o.Validate(cmd.Context(), sr, args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), sr)
		},
	}

	return cmd
}

// Complete captures the positional CRONJOB_ID.
func (o *cronJobDeleteOptions) Complete(ctx context.Context, args []string) error {
	o.CronJobID = args[0]
	return nil
}

// Validate ensures the CRONJOB_ID is present.
func (o *cronJobDeleteOptions) Validate(ctx context.Context, sr *runtime.StaticRuntime, args []string) error {
	if o.CronJobID == "" {
		return runtime.NewUserError("CRONJOB_ID 不能为空")
	}
	return nil
}

func (o *cronJobDeleteOptions) Run(ctx context.Context, sr *runtime.StaticRuntime) error {
	return cmdutil.DeleteScoped(ctx, sr.Store.CronJob(), "cronjob_id", o.CronJobID, "cronjob", "cronJobID", o.out)
}
