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

// Package job provides the "job" parent command and its create/get/list/update/
// delete subcommands over the job.job table (see internal/jobserver/model). It
// lets the chat Agent create and manage one-shot Jobs that oai-jobserver's
// "__builtin__" watcher polls and executes.
package job

import (
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	knownjob "github.com/onexstack/onexai/pkg/known/job"
)

// jobValidStatuses is the set of values callers may pass to "job update"'s
// --status flag. See pkg/known/job.JobStatusUnknown et al.
var jobValidStatuses = map[string]bool{
	knownjob.JobStatusUnknown: true,
	knownjob.JobStatusSuccess: true,
	knownjob.JobStatusFailed:  true,
}

// NewJobCmd creates the "job" parent command with create/get/list/update/delete
// subcommands for managing ad-hoc Job records.
func NewJobCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "管理 Job（一次性作业）：创建后由 Agent 立即执行一次的任务",
		Long: templates.LongDesc(`
			用途：管理 Job（一次性作业）。一个 Job 代表一次性的、交给 Agent 执行一次的任务：
			创建后由 __builtin__ watcher 轮询拾取，把 description 作为 prompt 交给 Agent 执行一次，
			结果回写到 Job 记录（有飞书上下文时推送回发起会话）。

			# 何时使用本工具
			用户需要"立即/尽快执行一次某个动作"时使用（例如"帮我发一次周运营数据"）。
			反之，只要出现"每天/每小时/每隔…/每周几/每月几号/定时/周期性"等重复时间信号，
			就必须改用 cronjob 工具，不要用 job。

			# 核心字段
			- description = 做什么：要 Agent 执行的具体动作，原样作为 prompt。只写"做什么"，
			  不含任何时间/频率（Job 是一次性的，本身没有"调度周期"的概念）。
			- watcher = 谁来做：执行该 Job 的 Agent 名称；留空则用平台默认 Agent（"__builtin__"）。

			# 重点提醒
			Job 没有 schedule。如果用户话里带执行周期/时间（"每天…"），说明这不该是 job，
			应改用 cronjob，并把时间写进 cronjob 的 --schedule，而不是写进 job 的 --description。

			# 限制
			每个用户最多 3 个一次性 Job（不含由 CronJob 触发产生的 Job），达上限需先删除再创建。

			# 子命令
			- create：创建一个 Job
			- get JOB_ID：按 ID 查询单条（含执行结果）
			- list：按用户/作用域/状态分页查询
			- update JOB_ID：局部更新（仅改显式传入的字段）
			- delete JOB_ID：删除单条`),
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		SilenceErrors:         true,
	}

	cmd.AddCommand(
		newJobCreateCmd(sr, out),
		newJobGetCmd(sr, out),
		newJobListCmd(sr, out),
		newJobUpdateCmd(sr, out),
		newJobDeleteCmd(sr, out),
	)

	return cmd
}
