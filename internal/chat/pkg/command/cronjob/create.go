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
	"time"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/command/cmdutil"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	jobmodel "github.com/onexstack/onexai/internal/jobserver/model"
	v1 "github.com/onexstack/onexai/pkg/api/jobserver/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
	knownjob "github.com/onexstack/onexai/pkg/known/job"
)

// cronJobTimeZone is the location used to stamp CreatedAt/UpdatedAt so the
// values render as RFC3339 with an explicit +08:00 offset regardless of the
// process's own timezone, which in container deployments is usually UTC.
var cronJobTimeZone = cmdutil.LoadShanghaiLocation()

type cronJobCreateOptions struct {
	out runtime.Responder

	Scope       string
	Name        string
	Description string
	Schedule    string
	Watcher     string
}

func newCronJobCreateCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &cronJobCreateOptions{out: out}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "创建一个周期性 CronJob：按 --schedule 反复触发，每次让 Agent 执行 --description",
		Long: templates.LongDesc(`
			用途：创建一个周期性 CronJob。创建后按 --schedule 反复触发，每次生成一个 Job，
			由 Agent 执行 --description 指定的动作。

			# 何时调用
			用户请求含任一重复时间信号时都要调用本命令，把任务落库，而不是只在对话里答复、
			也不要误用一次性 job：
			- "每天/每周/每月/每隔…"、"每小时/每5分钟"
			- "每周日/每周一/每个工作日"、"每天几点/每周几几点/每月几号几点"
			- "定时/定期/周期性/按计划"
			例："每周日上午11点干某事" → --description "干某事" --schedule "0 0 11 * * 0"。

			# 参数
			- --name（必填）：CronJob 名称，用于识别与后续查询。
			- --schedule（必填｜什么时候执行）：Cron 表达式或 robfig 宏。用户话里所有
			  频率/周期/星期几/几号/几点信息都转换到这里。
			- --description（必填｜执行什么）：具体动作，原样作为 prompt 交给 Agent，只写"做什么"。
			- --watcher（可选）：每次触发所产生 Job 的执行 Agent 名称，留空用默认 Agent（"__builtin__"）。

			# 重点提醒：调度时间只能写在 --schedule
			description 里绝对不能出现执行周期/时间。用户说"每天9点发周报"时，
			正确写法是 --description "发周报" --schedule "0 0 9 * * *"；
			把"每天"塞进 --description 会同时导致 prompt 语义错误和调度失效。

			# --schedule 写法参考（6 字段："分 时 日 月 周"；星期：0=周日 1=周一 … 6=周六）
			- "0 0 0 * * *" 或 "@daily"：每天 0 点
			- "0 0 9 * * *"：每天早上 9 点
			- "0 * * * * *" 或 "@hourly"：每小时
			- "* */5 * * * *"：每 5 分钟
			- "@every 3s"：每隔 3 秒（时长写法：@every 1m、@every 1h30m）
			- "0 0 11 * * 0"：每周日上午 11 点
			- "0 30 15 * * 3"：每周三下午 3 点半
			- "0 0 9 * * 1-5"：每个工作日（周一到周五）早上 9 点
			- "0 0 8 1 * *"：每月 1 号早上 8 点

			# 限制
			每个用户最多 3 个 CronJob，超出会创建失败并提示，需先删除已有的再创建。`),
		Example: templates.Examples(`
			# "每天早上9点发一次周运营数据"：动作放 description，时间放 schedule
			cronjob create --name daily-report --description "发周运营数据" --schedule "0 0 9 * * *"

			# "每周日上午11点干某事"：星期几放第 6 字段（周日=0）
			cronjob create --name sunday-task --description "干某事" --schedule "0 0 11 * * 0"

			# "每个工作日早上9点发晨报"：周一到周五写成 1-5
			cronjob create --name workday-report --description "发晨报" --schedule "0 0 9 * * 1-5"

			# "每小时巡检一次线上服务"：频率转成 @hourly
			cronjob create --name hourly-check --description "巡检线上服务健康状况" --schedule "@hourly"

			# "每3秒用 ops-agent 检查一次磁盘"：频率转成 @every 3s，并指定 Agent
			cronjob create --name check-disk --description "检查磁盘使用率" --schedule "@every 3s" --watcher ops-agent`),
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

	cmd.Flags().StringVar(&o.Scope, "scope", "", "CronJob 作用域，不填默认为 \"default\"。")
	cmd.Flags().StringVar(&o.Name, "name", "", "【必填】CronJob 名称。")
	cmd.Flags().StringVar(&o.Description, "description", "", descriptionFlagHelp)
	cmd.Flags().StringVar(&o.Schedule, "schedule", "", scheduleFlagHelp)
	cmd.Flags().StringVar(&o.Watcher, "watcher", "", "每次触发所产生 Job 的执行 Agent 名称，不填默认为平台默认 Agent（\"__builtin__\"）。")

	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("description")
	_ = cmd.MarkFlagRequired("schedule")

	return cmd
}

// Complete is a no-op for the cronjob create command (all inputs are flags).
func (o *cronJobCreateOptions) Complete(ctx context.Context, args []string) error {
	return nil
}

// Validate ensures the required flags are present.
func (o *cronJobCreateOptions) Validate(ctx context.Context, sr *runtime.StaticRuntime, args []string) error {
	if o.Name == "" || o.Description == "" || o.Schedule == "" {
		return runtime.NewUserError("--name、--description 和 --schedule 为必填项")
	}
	return nil
}

// Run inserts a new CronJob record. Description and Watcher are not only
// stored on the CronJobM row itself; they are also copied into JobTemplate,
// because the cronjob watcher builds each fired Job from JobTemplate alone and
// skips scheduling entirely when it is nil.
func (o *cronJobCreateOptions) Run(ctx context.Context, sr *runtime.StaticRuntime) error {
	// Capture the requester's Lark chat context so the __builtin__ watcher can
	// push each fired run's reply back to Lark (see JobTemplate.Params below)
	// instead of only writing it into the Job's Conditions in the DB. Only
	// populated for Lark-triggered invocations; empty for REST/MCP callers.
	dr := runtime.From(ctx)

	// Ownership is derived solely from the caller's identity; there is no
	// --username flag. Bail out with a clear message when the caller cannot be
	// identified rather than silently attributing the CronJob to a shared user.
	username, err := cmdutil.ResolveUsername(ctx)
	if err != nil {
		return err
	}

	// Cap the number of CronJobs per user so a single user cannot exhaust the
	// shared scheduler/agent resources.
	existing, _, err := sr.Store.CronJob().List(ctx, where.F("username", username))
	if err != nil {
		return fmt.Errorf("cronjob create: count existing cronjobs: %w", err)
	}
	if existing >= knownjob.MaxCronJobsPerUser {
		return fmt.Errorf("CronJob 数量已达上限（每个用户最多 %d 个），请先删除已有的 CronJob 再创建", knownjob.MaxCronJobsPerUser)
	}

	scope := o.Scope
	if scope == "" {
		scope = knownjob.DefaultJobScope
	}
	watcher := o.Watcher
	if watcher == "" {
		watcher = knownjob.DefaultWatcher
	}

	// Stamp CreatedAt/UpdatedAt explicitly in the UTC+8 zone.
	now := time.Now().In(cronJobTimeZone)

	obj := &jobmodel.CronJobM{
		Username:            username,
		Scope:               scope,
		Name:                o.Name,
		Description:         o.Description,
		Schedule:            o.Schedule, // Quartz-format schedule description for the CronJob
		ConcurrencyPolicy:   parallelConcurrencyPolicy,
		SuccessHistoryLimit: defaultSuccessHistoryLimit,
		FailedHistoryLimit:  defaultFailedHistoryLimit,
		CreatedAt:           now,
		UpdatedAt:           now,
		// Scope, Username, and CronJobID are filled in by the watcher itself for
		// each fired run. Name is copied verbatim here for consistency.
		JobTemplate: &jobmodel.JobM{
			Name:        o.Name,
			Description: o.Description,
			Watcher:     watcher,
			Status:      knownjob.JobStatusUnknown,
			Params: &jobmodel.JobParams{
				Agent: &v1.AgentParams{
					ReceiverType: dr.ReceiverType(),
					ReceiverID:   dr.Address(),
					IsSRE:        dr.IsSRE,
				},
			},
		},
	}

	if err := sr.Store.CronJob().Create(ctx, obj); err != nil {
		return fmt.Errorf("cronjob create: %w", err)
	}

	return o.out.SendResult(ctx, &runtime.CommandResult{Raw: obj})
}
