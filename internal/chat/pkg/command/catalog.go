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

package command

import (
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/onexstack/onexai/internal/chat/pkg/command/ai"
	"github.com/onexstack/onexai/internal/chat/pkg/command/core"
	"github.com/onexstack/onexai/internal/chat/pkg/command/cronjob"
	"github.com/onexstack/onexai/internal/chat/pkg/command/duty"
	"github.com/onexstack/onexai/internal/chat/pkg/command/job"
	"github.com/onexstack/onexai/internal/chat/pkg/command/platform"
	"github.com/onexstack/onexai/internal/chat/pkg/command/resource"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/pkg/agentlabel"
)

// LabelGroup is the label key recording which group a command belongs to. Its
// value is the Group.Key of the group the command is listed in; it is injected
// by AllLabels rather than declared per command, so a command's group is stated
// exactly once — where the command is listed.
//
// It is the key a binding writes into command_selector to grant a whole group:
// "group in (duty,ai)". A group is therefore not a separate field on the
// binding; it is an ordinary label every command already carries.
const LabelGroup = agentlabel.Prefix + "group"

// Group keys.
//
// A key is what lands in the database (inside a command_selector) and on a card
// button ("nav:group:<key>"), so it is frozen: renaming one strands every card
// already sent, whose buttons resolve the old key. The human-readable side —
// Message for the text help, Label for the card — may change freely.
const (
	// GroupCore holds the commands that report on the deployment itself.
	GroupCore = "core"
	// GroupDuty holds the on-call and ticket commands.
	GroupDuty = "duty"
	// GroupAI holds the commands that drive the assistant.
	GroupAI = "ai"
	// GroupMessaging holds the commands that send messages.
	GroupMessaging = "msg"
	// GroupJob holds the async-job and cron-job commands.
	GroupJob = "job"
	// GroupBuiltinAgent holds the commands that run a built-in agent. It is
	// declared but empty, so the group is ready before its first command.
	GroupBuiltinAgent = "builtin-agent"
	// GroupSkill holds the commands built at request time from the skills an
	// agent has exposed as commands (skill.as_cmd = "Yes").
	GroupSkill = "skill"
	// GroupCustom holds the user-defined commands an agent binds. Like skills,
	// they are built at request time; unlike skills, their grant is the binding
	// itself.
	GroupCustom = "custom"
	// GroupResource holds the commands that query infrastructure.
	GroupResource = "resource"
	// GroupPlatform holds the commands that drive third-party platforms.
	GroupPlatform = "platform"
)

// Entry is one command in the catalog.
type Entry struct {
	// Name is the command name: the first word of its cobra Use line, and the
	// value a binding writes into the commands list. It is declared rather than
	// derived so the tree can be assembled without building every command;
	// TestCatalog_EntryNames fails if it ever drifts from the built command.
	Name string
	// Short is the command's one-line description, the same text cobra would
	// print in its help listing. It is declared here for the same reason as
	// Name: the console lists the catalog without building every command, and
	// TestCatalog_EntriesMatchTheirCommands keeps this copy in step with the
	// built command's Short.
	Short string
	// Labels are this command's extra dimensions, e.g. {"scope": "readonly"}.
	// The group label is deliberately not set here — it comes from the group
	// the entry is listed in.
	//
	// Declare a key only when a binding actually needs to select on it: a label
	// no selector mentions is noise on the admin catalog, and it invites
	// operators to guess at values that select nothing.
	Labels labels.Set
	// New builds the command. It is a constructor rather than an instance
	// because the tree is rebuilt per request and each command captures that
	// request's responder.
	New func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command
	// AccountLevel marks a command that acts on the caller's account rather than
	// in the conversation, and is therefore built and granted unconditionally —
	// see chat.AccountLevel for why the agent grant table is the wrong axis for
	// it. It is declared here, on the entry, rather than read off the built
	// command, so that deciding whether to build a command does not require
	// building it: this loop runs on every message.
	AccountLevel bool
}

// Group is a named set of commands, and the only place either is declared.
//
// It carries all three of a group's identities at once, which is what keeps the
// text help and the card from drifting apart: earlier the help grouping lived
// here and the card grouping lived in each command's Annotations, and they had
// already diverged (pod was listed under the resource group but annotated Job).
type Group struct {
	// Key is the stable identifier bindings and card buttons carry.
	Key string
	// Message is the heading the text help prints, e.g. "Oncall 相关命令:" (on-call commands:).
	Message string
	// Label is the name the card shows, e.g. "值班" (duty).
	Label string
	// Entries are the group's commands, in the order they are listed.
	Entries []Entry
}

// catalog is the command catalog. It is static: the commands are compiled in,
// so this is a declaration rather than state.
var catalog = []Group{
	{
		Key:     GroupCore,
		Message: "基础命令:",
		Label:   "基础",
		Entries: []Entry{
			// /doctor reports on the deployment rather than on a conversation,
			// which is why it is grouped here and not with the AI commands.
			{
				Name:  "doctor",
				Short: "服务自检：报告当前配置、运行状态与需要留意的地方",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return core.NewDoctorCmd(sr, out)
				},
			},
			// /bind-onex is here for the same reason: it acts on the account
			// rather than in a conversation.
			//
			// It is not AccountLevel, and that is a deliberate change. The flag
			// exists for a command that must survive an empty grant table, and
			// binding is not that command: it is granted by a rule like every
			// other catalogued command, so which agents offer it is the
			// platform's decision rather than the bot's blanket one. The
			// platform makes that decision when it provisions an agent: the
			// OneX provisioner tags the agents it creates (its labels live
			// under onex.example.com/, see internal/pkg/agentlabel), and a
			// seeded rule turns the tag into the grant. The two audience
			// defaults are named in the same seed, because an unbound speaker's
			// conversation resolves to one of them and binding is exactly what
			// such a speaker needs — without them, removing the flag would take
			// the command away from the people it exists for. See
			// hack/onex-bind-command-seed.sql.
			{
				Name:  "bind-onex",
				Short: "绑定飞书账号：把控制台给出的绑定码发到这里",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return core.NewBindCmd(sr, out)
				},
			},
		},
	},
	{
		Key:     GroupDuty,
		Message: "Oncall 相关命令:",
		Label:   "值班",
		Entries: []Entry{
			{
				Name:  "oncall",
				Short: "创建并提交业务求助工单，或查询支持的值班模块名单",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewOncallCmd(sr, out)
				},
			},
			{
				Name:  "duty",
				Short: "查询当前值班人员/值班组名单 (On-call Duty)",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewDutyCmd(sr, out)
				},
			},
			{
				Name:  "close",
				Short: "关闭当前工单（自动更新状态并同步群名，此命令不接受参数）",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewCloseCmd(sr, out)
				},
			},
			{
				Name:  "invite",
				Short: "邀请指定模块的值班人员进入当前群组",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewInviteCmd(sr, out)
				},
			},
			{
				Name:  "hand",
				Short: "将当前工单转派给指定模块，并自动邀请该模块的值班人进群",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewHandCmd(sr, out)
				},
			},
			{
				Name:  "open",
				Short: "重新打开当前的已关闭工单（恢复为处理中状态）",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewOpenCmd(sr, out)
				},
			},
			{
				Name:  "save",
				Short: "保存当前工单群（防止自动解散），或取消保存",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewSaveCmd(sr, out)
				},
			},
			{
				Name:  "level",
				Short: "修改/设置当前工单的优先级级别 (Ticket Level)",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewLevelCmd(sr, out)
				},
			},
			{
				Name:  "assign",
				Short: "将当前工单指派给 @ 的人员处理",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewAssignCmd(sr, out)
				},
			},
			{
				Name:  "chat",
				Short: "查询当前Lark群组(工单)的值班人、群ID及流转状态",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewChatCmd(sr, out)
				},
			},
			{
				Name:  "mark",
				Short: "为当前工单添加处理备注并完成标记",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return duty.NewMarkCmd(sr, out)
				},
			},
		},
	},
	{
		Key:     GroupAI,
		Message: "AI 相关命令:",
		Label:   "AI 助手",
		Entries: []Entry{
			{
				Name:  "askai",
				Short: "向 AI 运维专家提问（支持通过 -r 参数开启深度推理模式）",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return ai.NewAskAICmd(sr, out)
				},
			},
			{
				Name:  "clear",
				Short: "清除当前对话的历史记忆（优先清除最旧的记录）",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return ai.NewClearCmd(sr, out)
				},
			},
			// /new and /switch carry the admin role: they are how a chat
			// conversation reaches the local process backend, so the gate that
			// keeps that off the open internet sits on the commands, not only
			// on the configuration.
			{
				Name:  "new",
				Short: "开始新对话（清空上下文，可指定 Agent 后端）",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return ai.NewNewCmd(sr, out)
				},
			},
			{
				Name:  "switch",
				Short: "切换当前对话的 Agent 后端（保留上下文）",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return ai.NewSwitchCmd(sr, out)
				},
			},
			// /dir decides which directory a local process runs in, so it sits
			// with the two commands that decide whether one runs at all.
			{
				Name:  "dir",
				Short: "查看或指定本对话的工作目录（仅管理员）",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return ai.NewDirCmd(sr, out)
				},
			},
			{
				Name:  "sessions",
				Short: "查看当前运行的会话概况（仅管理员、仅私聊）",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return ai.NewSessionsCmd(sr, out)
				},
			},
			{
				Name:  "aiassets",
				Short: "浏览或模糊查询已配置的 Agent、技能、MCP 服务器和知识库清单",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return ai.NewAIAssetsCmd(sr, out)
				},
			},
			{
				Name:  "agent",
				Short: "指定本会话使用的 Agent（管理员）",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return ai.NewAgentCmd(sr, out)
				},
			},
			{
				Name:  "ailab",
				Short: "以动态加载的技能/MCP/知识库增强 AI，然后进行推理。也可以用来进行 AI 能力的测试。",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return ai.NewAILabCmd(sr, out)
				},
			},
		},
	},
	{
		Key:     GroupMessaging,
		Message: "消息命令:",
		Label:   "消息",
		// msg.NewSendMsgCmd is kept commented out rather than deleted: the group
		// is declared so its key stays reserved and a binding naming it keeps
		// parsing.
	},
	{
		Key:     GroupJob,
		Message: "任务调度:",
		Label:   "任务调度",
		Entries: []Entry{
			{
				Name:  "job",
				Short: "管理 Job（一次性作业）：创建后由 Agent 立即执行一次的任务",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return job.NewJobCmd(sr, out)
				},
			},
			{
				Name:  "cronjob",
				Short: "管理 CronJob（周期性定时任务）：按 Cron 计划反复触发、每次生成一个 Job 交给 Agent 执行",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return cronjob.NewCronJobCmd(sr, out)
				},
			},
		},
	},
	{
		Key:     GroupBuiltinAgent,
		Message: "内置 Agent 命令:",
		Label:   "内置 Agent",
	},
	{
		Key:     GroupSkill,
		Message: "SKILL 相关命令:",
		Label:   "技能",
		// Entries are built per request from the skills the running agent has
		// exposed as commands; see skillEntries in skill.go.
	},
	{
		Key:     GroupCustom,
		Message: "自定义命令:",
		Label:   "自定义",
		// Entries are built per request from the commands the running agent
		// binds; see custom.go.
	},
	{
		Key:     GroupResource,
		Message: "资源查询命令:",
		Label:   "资源查询",
		Entries: []Entry{
			{
				Name:  "pod",
				Short: "查询 k8s Pod 集群、命名空间、IP 地址及资源用量",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return resource.NewPodCmd(sr, out)
				},
			},
		},
	},
	{
		Key:     GroupPlatform,
		Message: "平台命令:",
		Label:   "平台",
		Entries: []Entry{
			{
				Name:  "pipeline",
				Short: "触发 Zadig 自定义工作流（可指定服务与任务）",
				New: func(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
					return platform.NewPipelineCmd(sr, out)
				},
			},
		},
	},
}

var (
	// commandLabels is every command's full label set, built once: the catalog
	// is static, so this is a constant table. Every consumer — selector
	// matching, the admin catalog listing, group expansion — reads it, so a
	// command cannot have two answers to "what are your labels".
	commandLabels = buildCommandLabels()
	// commandNames is the set of every catalogued command name, for validating
	// a binding before it is stored.
	commandNames = buildCommandNames()
	// groupLabels maps a group key to the name a card shows for it.
	groupLabels = buildGroupLabels()
)

// Groups returns the command catalog.
func Groups() []Group { return catalog }

// GroupByKey returns the group with the given key.
func GroupByKey(key string) (Group, bool) {
	for _, g := range catalog {
		if g.Key == key {
			return g, true
		}
	}
	return Group{}, false
}

// GroupKeys returns every group key, in catalog order.
func GroupKeys() []string {
	keys := make([]string, 0, len(catalog))
	for _, g := range catalog {
		keys = append(keys, g.Key)
	}
	return keys
}

// GroupLabel returns the card label of the group with the given key. An unknown
// key is returned unchanged, which is what a card built before a group was
// removed still shows.
func GroupLabel(key string) string {
	if label, ok := groupLabels[key]; ok {
		return label
	}
	return key
}

// AllLabels returns every command's full label set, keyed by command name.
//
// The returned sets are the ones the selector matcher reads on the hot path, so
// callers must not modify them.
func AllLabels() map[string]labels.Set { return commandLabels }

// CommandNames returns the name of every catalogued command.
func CommandNames() map[string]struct{} { return commandNames }

// buildCommandLabels merges each entry's own labels with the key of the group it
// is listed in. The group key wins on conflict: a command's group is decided by
// where it is listed, never by its own label set.
func buildCommandLabels() map[string]labels.Set {
	var size int
	for _, g := range catalog {
		size += len(g.Entries)
	}
	out := make(map[string]labels.Set, size)
	for _, g := range catalog {
		for _, e := range g.Entries {
			ls := make(labels.Set, len(e.Labels)+1)
			for k, v := range e.Labels {
				ls[k] = v
			}
			ls[LabelGroup] = g.Key
			out[e.Name] = ls
		}
	}
	return out
}

// buildCommandNames collects every catalogued command name.
func buildCommandNames() map[string]struct{} {
	out := make(map[string]struct{})
	for _, g := range catalog {
		for _, e := range g.Entries {
			out[e.Name] = struct{}{}
		}
	}
	return out
}

// buildGroupLabels indexes each group's card label by its key.
func buildGroupLabels() map[string]string {
	out := make(map[string]string, len(catalog))
	for _, g := range catalog {
		out[g.Key] = g.Label
	}
	return out
}
