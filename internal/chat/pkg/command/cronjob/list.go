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

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/command/cmdutil"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	jobmodel "github.com/onexstack/onexai/internal/jobserver/model"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

type cronJobListOptions struct {
	out runtime.Responder

	Scope    string
	Page     int
	PageSize int
}

type cronJobListResult struct {
	Total int64                `json:"total"`
	Items []*jobmodel.CronJobM `json:"items"`
}

func newCronJobListCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &cronJobListOptions{out: out}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "分页查询当前用户的 CronJob 列表：可按作用域过滤",
		Long: templates.LongDesc(`
			用途：分页查询当前调用者本人的 CronJob 列表，只会返回属于你自己的记录。

			# 过滤/分页参数（均可选）
			- --scope：按作用域过滤
			- --page / --page-size：分页，page 从 1 开始，page-size 不填则不限制`),
		Example: templates.Examples(`
			# 列出自己的全部 CronJob
			cronjob list

			# 每页 10 条
			cronjob list --page 1 --page-size 10`),
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		SilenceErrors:         true,
		Annotations: map[string]string{
			chat.ChatEnabled: chat.True,
			chat.ChatGroup:   chat.ChatGroupCronJob,
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
	cmd.Flags().IntVar(&o.Page, "page", 1, "页码，从 1 开始。")
	cmd.Flags().IntVar(&o.PageSize, "page-size", 0, "每页数量，不填则不限制。")

	return cmd
}

// Complete is a no-op for the cronjob list command (no positional arguments).
func (o *cronJobListOptions) Complete(ctx context.Context, args []string) error {
	return nil
}

// Validate is a no-op for the cronjob list command (all filters are optional).
func (o *cronJobListOptions) Validate(ctx context.Context, sr *runtime.StaticRuntime, args []string) error {
	return nil
}

func (o *cronJobListOptions) Run(ctx context.Context, sr *runtime.StaticRuntime) error {
	// List is always scoped to the caller's own records; there is no --username
	// filter. Bail out when the caller cannot be identified.
	var kvs []any
	if o.Scope != "" {
		kvs = append(kvs, "scope", o.Scope)
	}
	scope, err := cmdutil.ScopeToCaller(ctx, kvs...)
	if err != nil {
		return err
	}

	opts := scope.P(o.Page, o.PageSize)

	total, items, err := sr.Store.CronJob().List(ctx, opts)
	if err != nil {
		return fmt.Errorf("cronjob list: %w", err)
	}

	return o.out.SendResult(ctx, &runtime.CommandResult{Raw: cronJobListResult{Total: total, Items: items}})
}
