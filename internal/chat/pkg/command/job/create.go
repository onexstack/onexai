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

// jobTimeZone is the location used to stamp CreatedAt/UpdatedAt so the values
// render as RFC3339 with an explicit +08:00 offset regardless of the process's
// own timezone, which in container deployments is usually UTC.
var jobTimeZone = cmdutil.LoadShanghaiLocation()

type jobCreateOptions struct {
	out runtime.Responder

	Scope       string
	Name        string
	Description string
	CronJobID   string
	Watcher     string
}

func newJobCreateCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &jobCreateOptions{out: out}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "创建一个一次性 Job：由 Agent 立即执行一次 --description 指定的动作",
		Long: templates.LongDesc(`
			用途：创建一个一次性 Job。创建后被 __builtin__ watcher 拾取，把 --description
			作为 prompt 交给 Agent 执行一次。

			# 何时调用
			用户需要"立即/现在/尽快做一次某事"时调用（例如"帮我发一次周运营数据"）。

			# 参数
			- --name（必填）：Job 名称，用于识别与后续查询。
			- --description（必填｜做什么）：要 Agent 执行的具体动作，原样作为 prompt。
			  只写"做什么"（例如"发周运营数据"），不要写任何执行时间或频率。
			- --watcher（可选）：执行该 Job 的 Agent 名称，留空用默认 Agent（"__builtin__"）。

			# 重点提醒：带时间/频率的需求不要用 job
			Job 是一次性的、没有 schedule。若用户说"每天/每小时/每隔多久执行一次"，
			请改用 cronjob create，并把周期写进 cronjob 的 --schedule，
			而不是塞进 job 的 --description。

			# 限制
			每个用户最多 3 个一次性 Job，超出会创建失败并提示，需先删除已有的再创建。`),
		Example: templates.Examples(`
			# "帮我发一次周运营数据"：默认 Agent 执行一次
			job create --name weekly-report --description "发周运营数据"

			# "现在检查一下磁盘使用率，用 ops-agent"：指定 Agent 执行一次
			job create --name check-disk --description "检查磁盘使用率" --watcher ops-agent`),
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

	cmd.Flags().StringVar(&o.Scope, "scope", "", "Job 作用域，不填默认为 \"default\"。")
	cmd.Flags().StringVar(&o.Name, "name", "", "Job 名称。")
	cmd.Flags().StringVar(&o.Description, "description", "", "【必填】用户希望执行的具体动作（例如\"发周运营数据\"），会作为 prompt 喂给 Agent。只写\"做什么\"，不要写任何执行时间或频率。")
	cmd.Flags().StringVar(&o.CronJobID, "cronjob-id", "", "可选，产生该 Job 的 CronJob ID。")
	cmd.Flags().StringVar(&o.Watcher, "watcher", "", "执行该 Job 的 Agent 名称，不填默认为平台默认 Agent（\"__builtin__\"）。")

	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("description")

	return cmd
}

// Complete is a no-op for the job create command (all inputs are flags).
func (o *jobCreateOptions) Complete(ctx context.Context, args []string) error {
	return nil
}

// Validate ensures the required flags are present.
func (o *jobCreateOptions) Validate(ctx context.Context, sr *runtime.StaticRuntime, args []string) error {
	if o.Name == "" || o.Description == "" {
		return runtime.NewUserError("--name 和 --description 为必填项")
	}
	return nil
}

func (o *jobCreateOptions) Run(ctx context.Context, sr *runtime.StaticRuntime) error {
	scope := o.Scope
	if scope == "" {
		scope = knownjob.DefaultJobScope
	}
	watcher := o.Watcher
	if watcher == "" {
		watcher = knownjob.DefaultWatcher
	}

	// Capture the requester's Lark chat context so the __builtin__ watcher can
	// push this Job's reply back to Lark once executed (see cronjob create for
	// the same pattern). Only populated for Lark-triggered invocations; empty
	// for REST/MCP callers.
	dr := runtime.From(ctx)

	// Ownership is derived solely from the caller's identity; there is no
	// --username flag. Bail out with a clear message when the caller cannot be
	// identified rather than silently attributing the Job to a shared user.
	username, err := cmdutil.ResolveUsername(ctx)
	if err != nil {
		return err
	}

	// Cap the number of ad-hoc Jobs (no owning CronJob) per user so a single
	// user cannot exhaust the shared scheduler/agent resources. Jobs spawned by
	// a CronJob carry a non-null cronjob_id and are excluded from this count.
	adhoc, _, err := sr.Store.Job().List(ctx, where.F("username", username).Q("cronjob_id IS NULL"))
	if err != nil {
		return fmt.Errorf("job create: count existing jobs: %w", err)
	}
	if adhoc >= knownjob.MaxJobsPerUser {
		return fmt.Errorf("Job 数量已达上限（每个用户最多 %d 个），请先删除已有的 Job 再创建", knownjob.MaxJobsPerUser)
	}

	// Stamp CreatedAt/UpdatedAt explicitly in the UTC+8 zone. Without this the
	// generated model leaves them as the zero time.Time (0001-01-01T00:00:00Z),
	// since the store's generic Create does not populate them and the column's
	// CURRENT_TIMESTAMP default is not read back into the in-memory struct.
	now := time.Now().In(jobTimeZone)

	obj := &jobmodel.JobM{
		Username:    username,
		Scope:       scope,
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
		CreatedAt: now,
		UpdatedAt: now,
	}
	if o.CronJobID != "" {
		obj.CronJobID = &o.CronJobID
	}

	if err := sr.Store.Job().Create(ctx, obj); err != nil {
		return fmt.Errorf("job create: %w", err)
	}

	return o.out.SendResult(ctx, &runtime.CommandResult{Raw: obj})
}
