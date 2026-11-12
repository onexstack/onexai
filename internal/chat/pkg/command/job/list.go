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

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/command/cmdutil"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	jobmodel "github.com/onexstack/onexai/internal/jobserver/model"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

type jobListOptions struct {
	out runtime.Responder

	Scope     string
	Status    string
	CronJobID string
	Page      int
	PageSize  int
}

type jobListResult struct {
	Total int64            `json:"total"`
	Items []*jobmodel.JobM `json:"items"`
}

func newJobListCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &jobListOptions{out: out}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "分页查询当前用户的 Job 列表：可按作用域/状态/来源 CronJob 过滤",
		Long: templates.LongDesc(`
			用途：分页查询当前调用者本人的 Job 列表，只会返回属于你自己的记录。

			# 过滤/分页参数（均可选）
			- --scope：按作用域过滤
			- --status：按状态过滤，取值 unknown（未执行）、success（成功）、failed（失败）
			- --cronjob-id：按产生该 Job 的 CronJob ID 过滤
			- --page / --page-size：分页，page 从 1 开始，page-size 不填则不限制`),
		Example: templates.Examples(`
			# 列出自己的全部 Job
			job list

			# 只看自己失败的 Job
			job list --status failed

			# 查看某个 CronJob 触发产生的（属于自己的）所有 Job
			job list --cronjob-id cronjob-tyw0g4`),
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		SilenceErrors:         true,
		Annotations: map[string]string{
			chat.ChatEnabled: chat.True,
			chat.ChatGroup:   chat.ChatGroupJob,
			chat.SREOnly:     chat.True,
			chat.MCPTool:     chat.True,
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

	cmd.Flags().StringVar(&o.Scope, "scope", "", "按作用域筛选。")
	cmd.Flags().StringVar(&o.Status, "status", "", "按状态筛选：unknown（未执行）、success（成功）、failed（失败）。")
	cmd.Flags().StringVar(&o.CronJobID, "cronjob-id", "", "按产生该 Job 的 CronJob ID 筛选。")
	cmd.Flags().IntVar(&o.Page, "page", 1, "页码，从 1 开始。")
	cmd.Flags().IntVar(&o.PageSize, "page-size", 0, "每页数量，不填则不限制。")

	return cmd
}

// Complete is a no-op for the job list command (no positional arguments).
func (o *jobListOptions) Complete(ctx context.Context, args []string) error {
	return nil
}

// Validate is a no-op for the job list command (all filters are optional).
func (o *jobListOptions) Validate(ctx context.Context, sr *runtime.StaticRuntime, args []string) error {
	return nil
}

func (o *jobListOptions) Run(ctx context.Context, sr *runtime.StaticRuntime) error {
	// List is always scoped to the caller's own records; there is no --username
	// filter. Bail out when the caller cannot be identified.
	var kvs []any
	if o.Scope != "" {
		kvs = append(kvs, "scope", o.Scope)
	}
	if o.Status != "" {
		kvs = append(kvs, "status", o.Status)
	}
	if o.CronJobID != "" {
		kvs = append(kvs, "cronjob_id", o.CronJobID)
	}

	scope, err := cmdutil.ScopeToCaller(ctx, kvs...)
	if err != nil {
		return err
	}

	opts := scope.P(o.Page, o.PageSize)

	total, items, err := sr.Store.Job().List(ctx, opts)
	if err != nil {
		return fmt.Errorf("job list: %w", err)
	}

	return o.out.SendResult(ctx, &runtime.CommandResult{Raw: jobListResult{Total: total, Items: items}})
}
