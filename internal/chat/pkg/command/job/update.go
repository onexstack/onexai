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

package job

import (
	"context"
	"fmt"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/command/cmdutil"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
	knownjob "github.com/onexstack/onexai/pkg/known/job"
)

type jobUpdateOptions struct {
	out runtime.Responder

	JobID       string
	Name        string
	Description string
	Watcher     string
	Suspend     int32
	Status      string
}

func newJobUpdateCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &jobUpdateOptions{out: out}

	cmd := &cobra.Command{
		Use:   "update <job_id>",
		Short: "局部更新指定 Job：JOB_ID 为位置参数，仅修改显式传入的字段",
		Long: templates.LongDesc(`
			用途：更新一个已存在的 Job。JOB_ID 用位置参数传入，只会修改本次显式传入的字段。

			# 参数
			- JOB_ID（位置参数，必填）：要更新的 Job ID。
			- --description（做什么）：新的动作描述（Agent 的 prompt），只写"做什么"，不含时间。
			- --name / --watcher / --suspend：按需修改对应字段。
			- --status：新的执行状态，取值 unknown、success、failed。

			# 提醒
			Job 没有 schedule，不要在 --description 里写执行时间/频率；有周期需求请用 cronjob。`),
		Example: templates.Examples(`
			# 把描述改成检查内存
			job update job-123 --description "检查内存使用率"

			# 手动把状态标记为成功
			job update job-123 --status success`),
		Args:                  cobra.ExactArgs(1),
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		SilenceErrors:         true,
		Annotations: map[string]string{
			// See the note in get.go: the <job_id> token in Use is what makes
			// this argument visible to cobrax.
			cobrax.AnnotationArgPrefix + "0": "要更新的 Job ID，如 job-123。",
			chat.ChatEnabled:                 chat.True,
			chat.ChatGroup:                   chat.ChatGroupJob,
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

	cmd.Flags().StringVar(&o.Name, "name", "", "新的 Job 名称。")
	cmd.Flags().StringVar(&o.Description, "description", "", "新的任务描述（喂给 Agent 的 prompt）。只写\"做什么\"，不要写执行时间或频率。")
	cmd.Flags().StringVar(&o.Watcher, "watcher", "", "新的执行 Agent 名称。")
	cmd.Flags().Int32Var(&o.Suspend, "suspend", 0, "新的挂起状态：1=挂起，0=不挂起。")
	cmd.Flags().StringVar(&o.Status, "status", "", "新的 Job 状态：unknown、success 或 failed。")

	return cmd
}

// Complete captures the positional JOB_ID.
func (o *jobUpdateOptions) Complete(ctx context.Context, args []string) error {
	o.JobID = args[0]
	return nil
}

// Validate ensures the JOB_ID is present and the status (when set) is valid.
func (o *jobUpdateOptions) Validate(ctx context.Context, sr *runtime.StaticRuntime, args []string) error {
	if o.JobID == "" {
		return runtime.NewUserError("JOB_ID 不能为空")
	}
	if o.Status != "" && !jobValidStatuses[o.Status] {
		return runtime.NewUserError("job update: invalid status %q: must be one of %q, %q, %q",
			o.Status, knownjob.JobStatusUnknown, knownjob.JobStatusSuccess, knownjob.JobStatusFailed)
	}
	return nil
}

func (o *jobUpdateOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, cmd *cobra.Command) error {
	// scopeToCaller pins the lookup to the caller's own rows, so updating a Job
	// owned by another user fails as "not found" and never mutates it.
	scope, err := cmdutil.ScopeToCaller(ctx, "job_id", o.JobID)
	if err != nil {
		return err
	}
	obj, err := sr.Store.Job().Get(ctx, scope)
	if err != nil {
		return fmt.Errorf("job update: job %q not found or you don't have permission: %w", o.JobID, err)
	}

	flags := cmd.Flags()
	if flags.Changed("name") {
		obj.Name = o.Name
	}
	if flags.Changed("description") {
		obj.Description = o.Description
	}
	if flags.Changed("watcher") {
		obj.Watcher = o.Watcher
	}
	if flags.Changed("suspend") {
		obj.Suspend = o.Suspend
	}
	if flags.Changed("status") {
		obj.Status = o.Status
	}

	if err := sr.Store.Job().Update(ctx, obj); err != nil {
		return fmt.Errorf("job update: %w", err)
	}

	return o.out.SendResult(ctx, &runtime.CommandResult{Raw: obj})
}
