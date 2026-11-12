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
	"fmt"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/command/cmdutil"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	jobmodel "github.com/onexstack/onexai/internal/jobserver/model"
	chat "github.com/onexstack/onexai/pkg/known/chat"
	knownjob "github.com/onexstack/onexai/pkg/known/job"
)

type cronJobUpdateOptions struct {
	out runtime.Responder

	CronJobID   string
	Scope       string
	Name        string
	Description string
	Schedule    string
	Watcher     string
	Suspend     int32
}

func newCronJobUpdateCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &cronJobUpdateOptions{out: out}

	cmd := &cobra.Command{
		Use:   "update <cronjob_id>",
		Short: "局部更新指定 CronJob：CRONJOB_ID 为位置参数，仅修改显式传入的字段",
		Long: templates.LongDesc(`
			用途：更新一个已存在的 CronJob。CRONJOB_ID 用位置参数传入，只会修改本次显式传入的
			字段，其余字段保持不变。

			# 参数
			- CRONJOB_ID（位置参数，必填）：要更新的 CronJob ID。
			- --schedule（什么时候执行）：新的 Cron 表达式或 robfig 宏。改执行周期/时间时用它。
			- --description（执行什么）：新的动作描述（Agent 的 prompt），只写"做什么"。
			- --name / --watcher / --suspend / --scope：按需修改对应字段。

			# 重点提醒：调度时间只能改 --schedule
			要调整"多久执行一次/几点执行"时，只能修改 --schedule，绝不能写进 --description。
			例："改成每天下午6点" → --schedule "0 18 * * *"（不要动 --description）。

			# --schedule 写法参考（5 字段："分 时 日 月 周"；星期：0=周日 … 6=周六）
			- "0 9 * * *"：每天早上 9 点        - "@hourly"：每小时
			- "*/5 * * * *"：每 5 分钟          - "0 11 * * 0"：每周日 11 点
			- "0 9 * * 1-5"：工作日 9 点         - "@every 3s"：每隔 3 秒`),
		Example: templates.Examples(`
			# "把这个定时任务改成每天下午6点执行"：只改 schedule
			cronjob update cronjob-tyw0g4 --schedule "0 18 * * *"

			# "把描述改成发月度报表"：只改 description（不含时间）
			cronjob update cronjob-tyw0g4 --description "发月度报表"

			# "暂停这个定时任务"：suspend=1 挂起
			cronjob update cronjob-tyw0g4 --suspend 1`),
		Args:                  cobra.ExactArgs(1),
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		SilenceErrors:         true,
		Annotations: map[string]string{
			// See the note in get.go: the <cronjob_id> token in Use is what
			// makes this argument visible to cobrax.
			cobrax.AnnotationArgPrefix + "0": "要更新的 CronJob ID，如 cronjob-tyw0g4。",
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
			return o.Run(cmd.Context(), sr, cmd)
		},
	}

	cmd.Flags().StringVar(&o.Scope, "scope", "", "新的作用域。")
	cmd.Flags().StringVar(&o.Name, "name", "", "新的 CronJob 名称。")
	cmd.Flags().StringVar(&o.Description, "description", "", "新的任务描述（喂给 Agent 的 prompt）。")
	cmd.Flags().StringVar(&o.Schedule, "schedule", "", scheduleFlagHelp)
	cmd.Flags().StringVar(&o.Watcher, "watcher", "", "新的执行 Agent 名称。")
	cmd.Flags().Int32Var(&o.Suspend, "suspend", 0, "新的挂起状态：1=挂起，0=不挂起。")

	return cmd
}

// Complete captures the positional CRONJOB_ID.
func (o *cronJobUpdateOptions) Complete(ctx context.Context, args []string) error {
	o.CronJobID = args[0]
	return nil
}

// Validate ensures the CRONJOB_ID is present.
func (o *cronJobUpdateOptions) Validate(ctx context.Context, sr *runtime.StaticRuntime, args []string) error {
	if o.CronJobID == "" {
		return runtime.NewUserError("CRONJOB_ID 不能为空")
	}
	return nil
}

// Run applies a partial update to an existing CronJob record. It loads the
// current row first and only overwrites fields the caller explicitly set
// (via cmd.Flags().Changed), since the underlying store.Update performs a
// full-row save. Description and Watcher are mirrored into JobTemplate,
// since that is what the cronjob watcher actually reads when it fires a run.
func (o *cronJobUpdateOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, cmd *cobra.Command) error {
	// scopeToCaller pins the lookup to the caller's own rows, so updating a
	// CronJob owned by another user fails as "not found" and never mutates it.
	scope, err := cmdutil.ScopeToCaller(ctx, "cronjob_id", o.CronJobID)
	if err != nil {
		return err
	}
	obj, err := sr.Store.CronJob().Get(ctx, scope)
	if err != nil {
		return fmt.Errorf("cronjob update: cronjob %q not found or you don't have permission: %w", o.CronJobID, err)
	}

	if obj.JobTemplate == nil {
		obj.JobTemplate = &jobmodel.JobM{Status: knownjob.JobStatusUnknown}
	}

	flags := cmd.Flags()
	if flags.Changed("scope") {
		obj.Scope = o.Scope
	}
	if flags.Changed("name") {
		obj.Name = o.Name
	}
	if flags.Changed("description") {
		obj.Description = o.Description
		obj.JobTemplate.Description = o.Description
	}
	if flags.Changed("schedule") {
		obj.Schedule = o.Schedule
	}
	if flags.Changed("watcher") {
		obj.JobTemplate.Watcher = o.Watcher
	}
	// ConcurrencyPolicy is fixed to 2 (parallel) for every CronJob.
	obj.ConcurrencyPolicy = parallelConcurrencyPolicy
	if flags.Changed("suspend") {
		obj.Suspend = o.Suspend
	}

	if err := sr.Store.CronJob().Update(ctx, obj); err != nil {
		return fmt.Errorf("cronjob update: %w", err)
	}

	return o.out.SendResult(ctx, &runtime.CommandResult{Raw: obj})
}
