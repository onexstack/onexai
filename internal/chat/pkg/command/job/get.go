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

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/command/cmdutil"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

type jobGetOptions struct {
	out   runtime.Responder
	JobID string
}

func newJobGetCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &jobGetOptions{out: out}

	cmd := &cobra.Command{
		Use:   "get <job_id>",
		Short: "查询单条 Job 的详情与执行结果：JOB_ID 为位置参数",
		Long: templates.LongDesc(`
			用途：按 ID 查询单条 Job 的完整详情，包括状态与执行结果。JOB_ID 用位置参数传入。
			仅能查询调用者本人拥有的 Job，否则报"未找到"。`),
		Example: templates.Examples(`
			# 查询指定 Job 的详情与执行结果
			job get job-123`),
		Args:                  cobra.ExactArgs(1),
		DisableFlagsInUseLine: true,
		SilenceUsage:          true,
		SilenceErrors:         true,
		Annotations: map[string]string{
			// The positional argument must be spelled <name> in Use for cobrax
			// to see it at all: it finds arguments with a regex over the tokens
			// after the command name, and a bare upper-case placeholder matches
			// nothing. Registered without this, the tool has no input property
			// and the model can never supply an ID.
			cobrax.AnnotationArgPrefix + "0": "要查询的 Job ID，如 job-123。",
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
			return o.Run(cmd.Context(), sr)
		},
	}

	return cmd
}

// Complete captures the positional JOB_ID.
func (o *jobGetOptions) Complete(ctx context.Context, args []string) error {
	o.JobID = args[0]
	return nil
}

// Validate ensures the JOB_ID is present.
func (o *jobGetOptions) Validate(ctx context.Context, sr *runtime.StaticRuntime, args []string) error {
	if o.JobID == "" {
		return runtime.NewUserError("JOB_ID 不能为空")
	}
	return nil
}

func (o *jobGetOptions) Run(ctx context.Context, sr *runtime.StaticRuntime) error {
	return cmdutil.GetScoped(ctx, sr.Store.Job(), "job_id", o.JobID, "job", o.out)
}
