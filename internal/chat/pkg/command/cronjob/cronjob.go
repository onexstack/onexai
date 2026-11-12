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

// Package cronjob provides the "cronjob" parent command and its create/get/
// list/update/delete subcommands over the job.cronjob table (see
// internal/jobserver/model). Like package job, it runs against chat's
// shared MySQL database so oai-jobserver's cronjob watcher — which fires each
// due CronJob by creating a new Job from its JobTemplate — actually sees the
// records created here.
package cronjob

import (
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// Default values applied by "cronjob create" when the caller omits the
// corresponding flag. Mirrors the removed internal/mcpserver/tool/cronjob.
const (
	// parallelConcurrencyPolicy is the fixed concurrency policy applied to every
	// CronJob on create/update: 2 = parallel (allow concurrent runs).
	parallelConcurrencyPolicy  int32 = 2
	defaultSuccessHistoryLimit int32 = 10
	defaultFailedHistoryLimit  int32 = 5
)

const scheduleFlagHelp = "【必填 | 什么时候执行】调度时间，只能写在这里，绝不能写进 --description。" +
	"格式为标准 6 字段 Cron 表达式\"秒 分 时 日 月 周\"（第 6 字段星期几：0=周日,1=周一,…,6=周六），" +
	"或 robfig 宏 @yearly/@monthly/@weekly/@daily/@hourly、@every <时长>（如 @every 3s、@every 1h30m）。" +
	"用户话里所有关于时间/频率/周期/星期几/几号/几点的信息，都必须转换成本字段。" +
	"示例：每天9点=\"0 0 9 * * *\"；每小时=\"@hourly\"；每5分钟=\"*/5 * * * *\"；" +
	"每周日11点=\"0 0 11 * * 0\"；工作日9点=\"0 0 9 * * 1-5\"；每月1号8点=\"0 0 8 1 * *\"；每隔3秒=\"@every 3s\"。"

const descriptionFlagHelp = "【必填 | 执行什么】要让 Agent 做的具体动作，会原样作为 prompt 交给 Agent。" +
	"这里只写\"做什么\"（例如\"发周运营数据\"、\"巡检线上服务健康状况\"），" +
	"严禁包含任何执行时间或频率词（如\"每天\"\"每小时\"\"每5分钟\"\"每周日11点\"）——那些只能放进 --schedule。"

// NewCronJobCmd creates the "cronjob" parent command with create/get/list/
// update/delete subcommands for managing scheduled CronJob records.
func NewCronJobCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cronjob",
		Short: "管理 CronJob（周期性定时任务）：按 Cron 计划反复触发、每次生成一个 Job 交给 Agent 执行",
		Long: templates.LongDesc(`
			用途：管理 CronJob（周期性定时任务）。一个 CronJob 会按 schedule 指定的 Cron 计划反复触发，
			每次触发生成一个 Job，由 Agent 执行 description 指定的动作。

			# 何时使用本工具
			只要用户请求里出现"重复执行的时间信号"，就用本工具（cronjob），而不是 job：
			- 频率类："每隔 N 秒/分/时/天/周/月"、"每小时"、"每5分钟"
			- 每天定点："每天上午9点"、"每天0点"
			- 每周定点："每周日11点"、"每逢周五"、"每个工作日9点"
			- 每月定点："每月1号8点"、"每月几号"
			- 定时口吻："定时/定期/周期性/按计划/循环/每次都…"
			反之，只有当用户明确表达"就执行一次/现在立刻做一次"且没有任何重复信号时，才改用 job 工具。
			判断不确定时，只要出现"每/定时/星期几/几号"等重复信号，就用 cronjob。

			# 两个核心字段（务必区分，切勿混淆）
			- schedule = 什么时候执行：标准 6 字段 Cron 表达式"分 时 日 月 周"，或 robfig 宏
			  （@daily、@hourly、@every 3s 等）。用户话里所有时间/频率/星期几/几号/几点的信息都放这里。
			  第 5 字段为星期几：0=周日, 1=周一, …, 6=周六。
			- description = 执行什么：要 Agent 做的具体动作，原样作为 prompt。只写"做什么"，不含任何时间。

			# 最容易犯的错误（重点提醒）
			绝对不要把调度时间/频率写进 description，它只能出现在 schedule。
			例："每天发周报" 必须拆成 description="发周报"、schedule="0 0 9 * * *"（或 @daily）。
			若把"每天"塞进 description，会同时导致 prompt 语义错误和调度失效。

			# 执行纪律
			- 对创建/修改/查看/删除定时任务的请求，必须实际调用本工具，不能只用自然语言声称"已创建"。
			- 必须等工具返回成功结果后再告知用户成功；调用失败时须把失败原因如实告诉用户。

			# 子命令
			- create：创建一个 CronJob
			- get CRONJOB_ID：按 ID 查询单条
			- list：按用户/作用域分页查询
			- update CRONJOB_ID：局部更新（仅改显式传入的字段）
			- delete CRONJOB_ID：删除单条并停止其后续调度`),
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		SilenceErrors:         true,
	}

	cmd.AddCommand(
		newCronJobCreateCmd(sr, out),
		newCronJobGetCmd(sr, out),
		newCronJobListCmd(sr, out),
		newCronJobUpdateCmd(sr, out),
		newCronJobDeleteCmd(sr, out),
	)

	return cmd
}
