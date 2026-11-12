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

package platform

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/clientset/typed/zadig"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	pipelineLong = templates.LongDesc(`
		该工具用于【触发 Zadig 平台的自定义工作流】，可指定工作流中的部分服务和部分任务（job）。

		【触发场景】：
		当用户需要发布某个服务时，如"帮我发布一下 xxx 流水线"、"用 xxx 流水线只构建一下"、
		"把 xxx 流水线部署到 prod 环境"，请调用此工具。

		【参数说明】：
		- <流水线名称>（位置参数，必填）：Zadig 中的工作流名称（workflow name），大小写敏感。
		  注意它是流水线本身的名字，不是服务名。
		- --service / -s：本次要构建的服务名，逗号分隔，可重复指定（如 -s gateway -s user）。
		  不指定表示工作流构建任务里配置的全部服务；服务名必须在工作流的构建任务里存在。
		  部署任务从构建任务取镜像时（source: fromjob），本次也只会部署这些服务。
		- --stage / -t：本次要执行的任务类型，逗号分隔。可选值为 build（构建）与 deploy（部署），
		  不指定表示执行工作流里的全部任务（包括构建、部署之外的自定义任务）。
		  任务顺序由工作流定义决定，调用方无需也无法指定顺序。
		- --ci：只执行构建任务，等价于 --stage build。不能与 --stage 同时指定。
		- --env / -e（必填）：目标环境，取值为工作流部署任务中配置的环境标识（如 edu-onex）。
		  它与工作流配置不一致时会直接报错，以免误以为发布到了别处。
		- --project / -p：Zadig 项目名，不指定时取服务端配置的默认项目。
		- --confirm：确认执行。不带该参数时只返回将要触发的参数预览，不会真正触发。

		【重要约束】：
		- 该命令会真正发起构建与部署，属于不可逆操作，必须显式加上 --confirm 才会执行。
		- Zadig 的自定义工作流任务由请求里点名的任务组成：没点名的任务不会执行。本命令据此实现
		  --ci / --stage——例如只构建时，部署任务根本不会出现在请求里。
		- 构建、部署之外的任务（插件、测试等）只可能出现在「不指定 --stage」的全量执行里，本命令不
		  为它们提供参数；这类任务的参数请在 Zadig 控制台确认。
		- 部署任务的服务来源必须是构建任务（source: fromjob），此时要部署的服务由 -s 与构建任务共同决定。
		  若工作流的部署任务不是从构建任务取服务，本命令无法通过 OpenAPI 说出它要部署哪些服务，会直接拒绝。
		- 该命令只触发流水线并返回任务 ID 与控制台链接，不会等待流水线执行完成。

		【返回值】：
		本工具返回一段 JSON，成功与失败都可据此判断，不必依赖自然语言描述：
		- triggered（布尔）：是否真的触发了。为 true 时必然同时给出 task_id 与 console_url。
		- task_id（字符串）：Zadig 任务 ID，仅当 triggered 为 true 时存在。
		- console_url（字符串）：该任务在 Zadig 控制台的链接，仅当 triggered 为 true 时存在。
		- target_env / services / stages / jobs：本次实际提交的目标环境与选中的服务、任务。
		- request_body：实际发给 Zadig 的请求体，可核对究竟点了哪些任务。
		- error（字符串）：仅当 triggered 为 false 时存在，说明未触发的原因。

		【禁止事项】：
		- 只有在本次工具返回中出现 triggered 为 true 与 task_id 之后，才可以对用户说"已触发"。
		  没有这个返回就等于没有触发：应当说明未触发及其原因（照抄 error 的内容），
		  不得推测或编造任务 ID，也不得声称"已提交""已开始构建"。
		- 不得自行加上 --confirm。默认行为是只返回参数预览；只有用户在本次对话中明确要求执行
		  （如"执行""触发""开始构建""发版"）时，才可以用 --confirm 真正触发。
		- 引用任务 ID 与控制台链接时必须原样照抄工具返回的值，不要改写、补齐或猜测其格式。`)

	pipelineExample = templates.Examples(`
		# 预览将要触发的参数（不会真正触发）
		/pipeline my-release-pipeline -e prod

		# 触发整条流水线（全部服务、全部任务）
		/pipeline my-release-pipeline -e prod --confirm

		# 只构建 gateway 与 user 两个服务
		/pipeline my-release-pipeline -e prod -s gateway,user --confirm

		# 只执行构建阶段（CI）
		/pipeline my-release-pipeline -e prod --ci --confirm

		# 只执行部署阶段
		/pipeline my-release-pipeline -e prod -t deploy --confirm

		# 正确的一次完整发布（两步）：先预览，等用户确认，再触发
		# 第一步（不带 --confirm，只预览，不会触发）：
		/pipeline my-release-pipeline -e prod -s gateway
		# 第二步（用户明确要求执行后，才加 --confirm）：
		/pipeline my-release-pipeline -e prod -s gateway --confirm`)
)

// PipelineOptions defines configuration for the "pipeline" command.
type PipelineOptions struct {
	out runtime.Responder
	sr  *runtime.StaticRuntime

	// PipelineName is the positional argument: a Zadig workflow name.
	PipelineName string
	// Services narrows the build; empty means every service the workflow's build
	// jobs are configured with.
	Services []string
	// Stages holds the raw --stage values, normalized by Validate into the job
	// types this run consists of.
	Stages []string
	// CIOnly is the --ci shorthand for "build only".
	CIOnly bool
	// TargetEnv is the --env value. Required: a release that picks the
	// environment by accident is the failure this flag exists to prevent.
	TargetEnv string
	// Project is the --project value, falling back to the configured default.
	Project string
	// Confirm must be set for the trigger to actually be sent.
	Confirm bool

	// req is the trigger Validate assembled from the workflow definition: the
	// jobs the created task will consist of, each with the parameters its type
	// needs. Run sends this and only this, so what was validated is what is sent.
	req *zadig.ExecuteCustomWorkflowRequest
}

// NewPipelineCmd creates the "pipeline" command.
func NewPipelineCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &PipelineOptions{out: out, sr: sr}

	cmd := &cobra.Command{
		Use:     "pipeline <pipeline_name>",
		Aliases: []string{"cicd"},
		Short:   "触发 Zadig 自定义工作流（可指定服务与任务）",
		Long:    pipelineLong,
		Example: pipelineExample,
		// SilenceUsage/SilenceErrors matter more here than elsewhere: the caller
		// may be an LLM reading the responder, so a validation failure must
		// surface as the message the command wrote, not as cobra's usage dump.
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		Annotations: map[string]string{
			// cobrax's keys drive the MCP tool schema; the chat x-* keys
			// drive this service's own surfaces (role gate, group, MCP inclusion).
			cobrax.AnnotationArgPrefix + "0": "【必填参数】Zadig 自定义工作流的名称（workflow name），不是服务名。",
			cobrax.AnnotationTitle:           "触发 Zadig 自定义工作流",
			cobrax.AnnotationReadOnly:        "false",
			cobrax.AnnotationDestructive:     "true",
			cobrax.AnnotationIdempotent:      "false",
			cobrax.AnnotationOpenWorld:       "true",
			chat.MCPTool:                     chat.True,
			chat.SREOnly:                     chat.True,
			chat.ChatGroup:                   chat.ChatGroupPlatform,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd.Context(), args); err != nil {
				return err
			}
			if err := o.Validate(cmd.Context(), args); err != nil {
				return o.fail(cmd.Context(), err)
			}
			return o.Run(cmd.Context())
		},
	}

	cmd.Flags().StringSliceVarP(&o.Services, "service", "s", nil,
		"要构建的服务名，逗号分隔且可重复指定（如 -s gateway,user）；部署任务从构建任务取镜像时也只会部署这些服务。不指定表示工作流里的全部服务")
	cmd.Flags().StringSliceVarP(&o.Stages, "stage", "t", nil,
		"要执行的任务类型，逗号分隔，可选 build/deploy（构建/部署）；不指定表示全部任务。与 --ci 互斥")
	cmd.Flags().BoolVar(&o.CIOnly, "ci", false,
		"只执行构建任务，等价于 --stage build；不能与 --stage 同时指定")
	cmd.Flags().StringVarP(&o.TargetEnv, "env", "e", "",
		"【必填】目标环境，必须与工作流部署任务里配置的环境一致（如 edu-onex）")
	// Declared required so the requirement reaches the MCP tool schema's
	// `required` list. Validate below still enforces it, but only for callers
	// that never pass through cobra's own flag validation (the REST path); a
	// model reading the schema has no other way to learn that a release without
	// an environment is refused, and the alternative — leaving it optional and
	// hoping the model asks — is how it came to invent one instead.
	_ = cmd.MarkFlagRequired("env")
	cmd.Flags().StringVarP(&o.Project, "project", "p", "",
		"Zadig 项目名；不指定时取服务端配置的默认项目")
	cmd.Flags().BoolVar(&o.Confirm, "confirm", false,
		"确认执行（该开关不带值）。不指定时只返回参数预览，不会触发流水线")

	return cmd
}

// Complete captures the positional pipeline name and normalizes the lists.
func (o *PipelineOptions) Complete(ctx context.Context, args []string) error {
	o.PipelineName = args[0]
	o.Services = normalizeServices(o.Services)

	// The project has a deployment-level default; the environment deliberately
	// does not. Falling back to a configured environment would mean a release
	// whose target nobody stated, which is the one mistake this command must not
	// make quietly.
	if o.Project == "" {
		if client, err := o.client(); err == nil {
			o.Project = client.DefaultProject()
		}
	}
	return nil
}

// Validate resolves the selection and rejects the combinations that would make
// the trigger mean something the caller did not ask for.
func (o *PipelineOptions) Validate(ctx context.Context, args []string) error {
	// The static checks come first so a caller who mistyped a flag is told so
	// without a round trip to Zadig.
	if o.PipelineName == "" {
		return runtime.NewUserError("必须提供流水线名称（Zadig 工作流名称）")
	}

	build, deploy, err := resolvePhases(o.CIOnly, o.Stages)
	if err != nil {
		return runtime.NewUserError("%s", err.Error())
	}

	if o.TargetEnv == "" {
		return runtime.NewUserError("必须指定目标环境：请用 -e/--env 指定，如 -e edu-onex")
	}
	if o.Project == "" {
		return runtime.NewUserError("缺少 Zadig 项目名：请用 -p/--project 指定，或联系管理员配置 backend.zadig-project")
	}
	client, err := o.client()
	if err != nil {
		return runtime.NewUserError("%s", err.Error())
	}
	if !client.Configured() {
		return runtime.NewUserError("Zadig 未配置，请联系管理员设置 backend.zadig-endpoint")
	}

	return o.resolve(ctx, client, build, deploy)
}

// resolve reads the workflow definition and turns the flags into the request
// that will be sent.
//
// The definition decides what "valid" means here, because both ways this command
// can be wrong are silent ones: building the wrong services, and deploying
// something nobody asked for. Zadig composes a custom workflow's task from the
// jobs its inputs name — a job that is not named does not run — so the
// definition is the only authority on what -s and -e can mean, and a question it
// cannot answer is a refusal rather than a guess.
func (o *PipelineOptions) resolve(ctx context.Context, client zadig.Interface, build, deploy bool) error {
	detail, err := client.WorkflowDetail(ctx, o.Project, o.PipelineName)
	if err != nil {
		return runtime.NewUserError("读取 Zadig 工作流 %q 失败：%s", o.PipelineName, err.Error())
	}

	selected, err := o.selectJobs(detail, build, deploy)
	if err != nil {
		return err
	}
	if err := o.validateSelection(selected, detail); err != nil {
		return err
	}
	inputs, err := o.buildInputs(ctx, client, selected, detail.Jobs())
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return runtime.NewUserError("本次选择没有产生任何要执行的任务：请检查 -s 指定的服务是否属于工作流 %q", o.PipelineName)
	}

	o.req = &zadig.ExecuteCustomWorkflowRequest{
		ProjectName:  o.Project,
		WorkflowName: o.PipelineName,
		Inputs:       inputs,
	}
	return nil
}

// selectJobs narrows the workflow to the jobs this run asks for.
//
// Without --stage/--ci the whole workflow runs; a stage flag narrows the
// selection to the job types it names.
func (o *PipelineOptions) selectJobs(detail *zadig.WorkflowDetail, build, deploy bool) ([]zadig.Job, error) {
	all := !o.CIOnly && len(o.Stages) == 0
	var selected []zadig.Job
	for _, job := range detail.Jobs() {
		if !all {
			switch job.Type {
			case zadig.JobTypeBuild:
				if !build {
					continue
				}
			case zadig.JobTypeDeploy:
				if !deploy {
					continue
				}
			default:
				// A job type that is neither: --stage names build and deploy, so a
				// narrowed run leaves other jobs (a plugin or test job) out instead
				// of running them unasked. Only the whole-workflow run includes them.
				continue
			}
		}
		selected = append(selected, job)
	}
	if len(selected) == 0 {
		return nil, runtime.NewUserError(
			"工作流 %q 里没有本次选择的任务：--ci/--stage 只能选中 zadig-build（构建）与 zadig-deploy（部署）类型的任务",
			o.PipelineName,
		)
	}
	return selected, nil
}

// validateSelection checks -s and -e against the workflow's own definition,
// because both ways this command can be wrong are silent ones.
func (o *PipelineOptions) validateSelection(selected []zadig.Job, detail *zadig.WorkflowDetail) error {
	var buildJobs []zadig.Job
	selectedNames := make([]string, 0, len(selected))
	for _, job := range selected {
		selectedNames = append(selectedNames, job.Name)
		if job.Type == zadig.JobTypeBuild {
			buildJobs = append(buildJobs, job)
		}
	}

	// The service names are checked against the workflow rather than passed
	// through: a name Zadig does not know would be dropped from the build job's
	// service list, and a build that silently builds nothing looks exactly like a
	// build that succeeded.
	if len(o.Services) > 0 {
		if len(buildJobs) == 0 {
			return runtime.NewUserError(
				"--service 只作用于构建任务，但本次选择里没有构建任务：请用 --stage build 或不加 --stage",
			)
		}
		known := serviceNames(buildJobs)
		for _, name := range o.Services {
			if !slices.Contains(known, name) {
				return runtime.NewUserError("工作流 %q 的构建任务里没有服务 %q；可选服务：%s",
					o.PipelineName, name, strings.Join(known, ", "))
			}
		}
	}

	// -e is checked against the workflow's own environments, not merely passed
	// through: the input it feeds becomes the deploy job's environment, so a value
	// that disagrees would deploy somewhere the workflow never intended, and a
	// value that agrees is the caller stating the target out loud.
	//
	// This check covers the whole workflow rather than the selection, so a
	// build-only run validates -e the same way a full run does — otherwise an
	// environment the workflow does not have would be accepted for --ci and
	// refused for the run that actually deploys.
	var envs []string
	for _, job := range detail.Jobs() {
		if job.Type != zadig.JobTypeDeploy {
			continue
		}
		if env := job.Env(); env != "" && !slices.Contains(envs, env) {
			envs = append(envs, env)
		}
	}
	if len(envs) > 0 && !slices.Contains(envs, o.TargetEnv) {
		return runtime.NewUserError("工作流 %q 的部署环境是 %s，与 --env %q 不一致；请改用 -e %s",
			o.PipelineName, strings.Join(envs, ", "), o.TargetEnv, envs[0])
	}

	for _, job := range selected {
		if job.Type != zadig.JobTypeDeploy {
			continue
		}

		// A selected deploy job with its own environment still has to agree: a
		// workflow that deploys to two environments would otherwise be sent an
		// environment belonging to the other one.
		if env := job.Env(); env != "" && env != o.TargetEnv {
			return runtime.NewUserError("工作流 %q 的部署任务 %q 目标是环境 %q，与 --env %q 不一致；请改用 -e %s",
				o.PipelineName, job.Name, env, o.TargetEnv, env)
		}

		// A deploy job whose images come from another job cannot run without it:
		// this request carries no image references of its own, so a skipped build
		// would leave it with nothing to deploy.
		if src := job.ImageSourceJob(); src != "" && !slices.Contains(selectedNames, src) {
			return runtime.NewUserError(
				"工作流 %q 的部署任务 %q 从构建任务 %q 取镜像（source: fromjob），本次选择里没有它，Zadig 无法确定要部署的镜像；请用 --stage build（只构建）或不加 --stage（构建+部署）",
				o.PipelineName, job.Name, src,
			)
		}
	}
	return nil
}

// buildInputs turns the selected jobs into the request's inputs, resolving the
// registry address and the services each job actually runs.
func (o *PipelineOptions) buildInputs(
	ctx context.Context, client zadig.Interface, selected, allJobs []zadig.Job,
) ([]zadig.JobInput, error) {
	inputs := make([]zadig.JobInput, 0, len(selected))
	registries := make(map[string]string)
	for _, job := range selected {
		in := zadig.JobInput{JobName: job.Name, JobType: job.Type}

		switch job.Type {
		case zadig.JobTypeBuild:
			registryID := job.RegistryID()
			if registryID == "" {
				return nil, runtime.NewUserError("工作流 %q 的构建任务 %q 没有配置镜像仓库（docker_registry_id 为空），无法通过 OpenAPI 触发",
					o.PipelineName, job.Name)
			}
			addr, ok := registries[registryID]
			if !ok {
				// Zadig matches a build input's registry against "<address>/<namespace>"
				// and rejects anything else, so the id a job spec carries has to be
				// resolved rather than sent.
				var err error
				addr, err = client.RegistryAddress(ctx, registryID)
				if err != nil {
					return nil, runtime.NewUserError("解析镜像仓库 %s 失败：%s", registryID, err.Error())
				}
				registries[registryID] = addr
			}

			services := o.servicesFor(job)
			if len(services) == 0 {
				// Every -s name belongs to another build job, so this one has
				// nothing to build: leaving it out keeps an empty job (and its
				// build) out of the task.
				continue
			}
			in.Parameters = map[string]any{
				"registry":     addr,
				"service_list": serviceInputs(services),
			}
		case zadig.JobTypeDeploy:
			services, err := o.deployServices(job, allJobs)
			if err != nil {
				return nil, err
			}
			in.Parameters = map[string]any{
				"env_name":     o.TargetEnv,
				"service_list": serviceInputs(services),
			}
		}

		inputs = append(inputs, in)
	}
	return inputs, nil
}

// servicesFor returns the services of one build job that this run builds: the
// caller's -s selection narrowed to the job's own services, or every service the
// job is configured with when no selection was given.
func (o *PipelineOptions) servicesFor(job zadig.Job) []zadig.BuildService {
	configured := job.BuildServices()
	if len(o.Services) == 0 {
		return configured
	}

	var out []zadig.BuildService
	for _, svc := range configured {
		if slices.Contains(o.Services, svc.ServiceName) {
			out = append(out, svc)
		}
	}
	return out
}

// deployServices returns the services a deploy job's input has to name.
//
// Zadig replaces a deploy job's own service list with the one its input carries,
// so that list is the deployment's selection rather than a narrowing of it:
// leaving it out says "deploy nothing", which Zadig refuses outright ("deploy
// service is empty for env ..."). A job fed by a build job (source: fromjob) can
// only deploy that build job's own services — Zadig resolves each service's
// image from the build job's $IMAGE output, so a service the build job does not
// build has no image to deploy — and it takes the same -s narrowing the build
// job itself takes.
func (o *PipelineOptions) deployServices(job zadig.Job, jobs []zadig.Job) ([]zadig.BuildService, error) {
	origin := job.ImageSourceJob()
	if origin == "" {
		// Nothing else can name the services: a deploy job that does not take them
		// from a build job is configured with them (or picks them) in Zadig itself,
		// which this request has no way to read.
		return nil, runtime.NewUserError(
			"工作流 %q 的部署任务 %q 的服务来源不是构建任务（spec.source 不是 fromjob），OpenAPI 触发无法说出它要部署哪些服务；请用 --stage build（只构建），或让工作流管理员把该部署任务的服务来源改成构建任务",
			o.PipelineName, job.Name,
		)
	}

	for _, candidate := range jobs {
		if candidate.Name != origin || candidate.Type != zadig.JobTypeBuild {
			continue
		}

		services := o.servicesFor(candidate)
		if len(services) == 0 {
			// The build job is in the task but builds nothing this run, so the
			// deploy would be handed an empty service list — the very refusal this
			// function exists to prevent, one step earlier and with a better reason.
			return nil, runtime.NewUserError(
				"工作流 %q 的部署任务 %q 从构建任务 %q 取服务，但本次 -s 选择里没有该构建任务要构建的服务，部署将没有服务可上；请检查 -s 指定的服务是否属于构建任务 %q",
				o.PipelineName, job.Name, origin, origin,
			)
		}
		return services, nil
	}

	return nil, runtime.NewUserError(
		"工作流 %q 的部署任务 %q 引用的构建任务 %q 不在工作流定义里，无法确定它要部署哪些服务",
		o.PipelineName, job.Name, origin,
	)
}

// Run triggers the workflow, or reports what it would have triggered.
func (o *PipelineOptions) Run(ctx context.Context) error {
	client, err := o.client()
	if err != nil {
		return o.fail(ctx, err)
	}
	if o.req == nil {
		// Unreachable through Validate, and handled rather than ignored: a nil
		// request would otherwise be a trigger that silently does nothing.
		return o.fail(ctx, fmt.Errorf("内部错误：未生成 Zadig 触发请求"))
	}
	req := o.req

	// Not confirmed is a rejection, not a silent no-op: it returns a user error
	// so the invocation is recorded as refused, but it still renders the preview
	// on every surface so the caller can re-run the same command with --confirm.
	// The preview echoes the exact body too — for a caller who is unsure whether
	// "no --service" means every service, or whether "build only" really leaves
	// the deploy job out, that is the answer.
	if !o.Confirm {
		preview := fmt.Sprintf("未确认：未加 --confirm，未触发。确认请执行：\n`/pipeline %s -e %s%s --confirm`",
			o.PipelineName, o.TargetEnv, o.flagSuffix())
		return o.failWith(ctx, req, runtime.NewUserError("未加 --confirm，流水线未触发（这是预览）:\n%s", preview))
	}

	task, err := client.ExecuteCustomWorkflow(ctx, req)
	if err != nil {
		// The request is echoed along with the failure: it was sent, and what
		// Zadig rejected is the first thing the operator needs to see.
		return o.failWith(ctx, req, fmt.Errorf("触发 Zadig 流水线失败: %w", err))
	}

	consoleURL := client.ConsoleURL(o.Project, o.PipelineName, task.TaskID)

	// The plain-text representation has no markup to carry the link, so it is
	// appended explicitly.
	hint := ""
	if consoleURL != "" {
		hint = "链接：" + consoleURL
	}
	return o.out.SendResult(ctx, &runtime.CommandResult{
		Raw: o.raw(req, task, consoleURL),
		Markdown: &runtime.MarkdownContent{
			Title:   fmt.Sprintf("已触发 Zadig 流水线：%s", o.PipelineName),
			Content: o.render(task, consoleURL),
			Color:   "green",
		},
		PlainText: fmt.Sprintf("已触发 Zadig 流水线 %s，任务 ID %d。%s",
			o.PipelineName, task.TaskID, hint),
	})
}

// fail renders the failure into the responder and returns err.
func (o *PipelineOptions) fail(ctx context.Context, err error) error {
	return o.failWith(ctx, nil, err)
}

// failWith is fail's body, taking the request the failure is about when there is
// one so the reply can echo what was (or would have been) sent.
//
// The returned error alone is not enough: the REST and MCP paths drop it (the
// caller only logs it) and send back whatever the responder holds, so a command
// that only returned would answer those callers with an empty body. The Lark
// path renders the returned error itself, and sending a result there as well
// would reply twice — so on Lark the responder is left untouched.
func (o *PipelineOptions) failWith(ctx context.Context, req *zadig.ExecuteCustomWorkflowRequest, err error) error {
	dr := runtime.From(ctx)
	if o.out == nil || dr.IsLark() {
		return err
	}

	// There is no task to report, but the selection is echoed whenever there is
	// one: a refused request — whether it was refused before being sent or by
	// Zadig — is the first thing the caller has to look at. A failure raised
	// before a request existed (no client, a bad workflow name) echoes the flags
	// it was working from instead.
	raw := map[string]any{
		"triggered":     false,
		"project_name":  o.Project,
		"workflow_name": o.PipelineName,
		"target_env":    o.TargetEnv,
		"stages":        o.endpointStages(),
		"services":      o.displayServices(),
		"error":         err.Error(),
	}
	if req != nil {
		raw["jobs"] = jobsView(req)
		raw["request_body"] = zadig.CustomTaskBody(req)
	}
	_ = o.out.SendResult(ctx, &runtime.CommandResult{
		Raw: raw,
		Markdown: &runtime.MarkdownContent{
			Title:   "Zadig 流水线未触发",
			Content: err.Error(),
			Color:   "red",
		},
		PlainText: err.Error(),
	})
	return err
}

// raw builds the JSON representation shared by the API and MCP surfaces.
func (o *PipelineOptions) raw(req *zadig.ExecuteCustomWorkflowRequest, task *zadig.ExecuteWorkflowTask, consoleURL string) map[string]any {
	out := map[string]any{
		"triggered":     true,
		"project_name":  o.Project,
		"workflow_name": o.PipelineName,
		"target_env":    o.TargetEnv,
		"stages":        o.endpointStages(),
		"services":      o.displayServices(),
		// The jobs are the task: Zadig runs the ones named here and no others, so
		// the reply states them rather than leaving the caller to infer them from
		// the flags.
		"jobs": jobsView(req),
		// The encoded request is echoed so a caller can see exactly what was sent,
		// including which jobs it does not mention.
		"request_body": zadig.CustomTaskBody(req),
		"task_id":      task.TaskID,
		"console_url":  consoleURL,
	}
	if task.Raw != nil {
		out["zadig_response"] = task.Raw
	}
	return out
}

// render formats the trigger outcome as markdown.
func (o *PipelineOptions) render(task *zadig.ExecuteWorkflowTask, consoleURL string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Zadig 自定义工作流：%s\n\n", o.PipelineName)
	fmt.Fprintf(&sb, "- **项目**：`%s`\n", o.Project)
	fmt.Fprintf(&sb, "- **目标环境**：`%s`\n", o.TargetEnv)
	fmt.Fprintf(&sb, "- **任务**：\n")
	for _, job := range jobsView(o.req) {
		if services, ok := job["services"].([]string); ok {
			fmt.Fprintf(&sb, "  - `%s`（%s）：%s\n", job["job_name"], job["job_type"], strings.Join(services, ", "))
			continue
		}
		fmt.Fprintf(&sb, "  - `%s`（%s）\n", job["job_name"], job["job_type"])
	}
	fmt.Fprintf(&sb, "- **任务 ID**：`%d`\n", task.TaskID)
	if consoleURL != "" {
		fmt.Fprintf(&sb, "- **控制台链接**：[%d](%s)\n", task.TaskID, consoleURL)
	}
	sb.WriteString("\n已触发，未等待执行结果。")
	return sb.String()
}

// endpointStages names the job types in the request for the reply: what will run,
// rather than what was asked for.
func (o *PipelineOptions) endpointStages() []string {
	out := []string{}
	if o.req == nil {
		return out
	}
	for _, in := range o.req.Inputs {
		name := in.JobType
		switch name {
		case zadig.JobTypeBuild:
			name = "build"
		case zadig.JobTypeDeploy:
			name = "deploy"
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// jobsView renders the request's jobs, with the services each one builds.
func jobsView(req *zadig.ExecuteCustomWorkflowRequest) []map[string]any {
	if req == nil {
		return nil
	}

	out := make([]map[string]any, 0, len(req.Inputs))
	for _, in := range req.Inputs {
		job := map[string]any{
			"job_name": in.JobName,
			"job_type": in.JobType,
		}
		if services := inputServices(in); len(services) > 0 {
			job["services"] = services
		}
		out = append(out, job)
	}
	return out
}

// inputServices reads the services a job input names. It reads them back from
// the request rather than from the flags, because that is what the reply is
// about: the task Zadig will create.
func inputServices(in zadig.JobInput) []string {
	list, ok := in.Parameters["service_list"].([]map[string]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(list))
	for _, entry := range list {
		if name, ok := entry["service_name"].(string); ok && name != "" {
			out = append(out, name)
		}
	}
	return out
}

// serviceInputs renders build services for the wire.
//
// service_module is sent alongside the name because Zadig matches an input
// service on the pair: an entry whose module is missing matches nothing, and a
// build job whose service list matches nothing builds nothing. For a service
// whose module is not set the name stands in for it, which is how Zadig's own
// service model defaults it.
func serviceInputs(services []zadig.BuildService) []map[string]any {
	out := make([]map[string]any, 0, len(services))
	for _, svc := range services {
		module := svc.ServiceModule
		if module == "" {
			module = svc.ServiceName
		}
		out = append(out, map[string]any{
			"service_name":   svc.ServiceName,
			"service_module": module,
		})
	}
	return out
}

// serviceNames lists the service names the given build jobs are configured with,
// in definition order and without duplicates.
func serviceNames(jobs []zadig.Job) []string {
	var out []string
	for _, job := range jobs {
		for _, svc := range job.BuildServices() {
			if svc.ServiceName != "" && !slices.Contains(out, svc.ServiceName) {
				out = append(out, svc.ServiceName)
			}
		}
	}
	return out
}

// displayServices renders the service selection for humans, where "all" is a
// value and not an absence.
func (o *PipelineOptions) displayServices() any {
	if len(o.Services) == 0 {
		return "全部"
	}
	return o.Services
}

// flagSuffix echoes the flags the caller would need to add to a preview to make
// it an actual trigger.
func (o *PipelineOptions) flagSuffix() string {
	var sb strings.Builder
	if len(o.Services) > 0 {
		fmt.Fprintf(&sb, " -s %s", strings.Join(o.Services, ","))
	}
	if o.CIOnly {
		sb.WriteString(" --ci")
	} else if len(o.Stages) > 0 {
		fmt.Fprintf(&sb, " -t %s", strings.Join(o.Stages, ","))
	}
	if o.Project != "" {
		fmt.Fprintf(&sb, " -p %s", o.Project)
	}
	return sb.String()
}

// client resolves the Zadig client, failing loudly when the static runtime is
// missing so a misconfigured caller gets an error instead of a panic.
func (o *PipelineOptions) client() (zadig.Interface, error) {
	if o.sr == nil || o.sr.Clientset == nil {
		return nil, fmt.Errorf("Zadig 客户端不可用：服务端未完成初始化")
	}
	client := o.sr.Clientset.Zadig()
	if client == nil {
		return nil, fmt.Errorf("Zadig 客户端不可用：服务端未完成初始化")
	}
	return client, nil
}

// resolvePhases maps --ci and --stage onto the two job types a Zadig custom
// workflow's stages are made of.
//
// A custom workflow is a list of typed jobs, and the task's inputs name which of
// them to include, so "choose stages" means "choose which job types go into the
// request" — a type that is left out does not run at all. Order is not a caller
// concern: the jobs run in the order the workflow defines, whatever the caller
// asked for.
func resolvePhases(ci bool, stages []string) (build, deploy bool, err error) {
	// Refused rather than resolved by precedence: --ci is exactly "build only",
	// so --ci with an explicit stage list is a caller contradicting itself, and
	// letting one silently win could deploy when only a build was intended.
	if ci && len(stages) > 0 {
		return false, false, fmt.Errorf("--ci 与 --stage 不能同时指定，请二选一：--ci（仅构建）或 --stage build|deploy")
	}
	if ci {
		return true, false, nil
	}
	if len(stages) == 0 {
		return true, true, nil
	}

	for _, raw := range stages {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "build", "构建":
			build = true
		case "deploy", "部署":
			deploy = true
		case "":
			// A trailing comma in "build," yields an empty element; it carries no
			// meaning and is not worth rejecting the whole command for.
		default:
			return false, false, fmt.Errorf("--stage 只支持 build 或 deploy（构建/部署），收到：%s", raw)
		}
	}
	if !build && !deploy {
		return false, false, fmt.Errorf("--stage 未指定有效阶段，只支持 build 或 deploy（构建/部署）")
	}
	return build, deploy, nil
}

// normalizeServices trims, drops empties and de-duplicates the service names
// while preserving the caller's order, so the reply reads back the same way it
// was written.
func normalizeServices(in []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		for _, part := range strings.Split(raw, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}
