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

package ai

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	newLong = templates.LongDesc(`
        开始一个新的对话：忘记当前会话的上下文，并（可选）换一个 Agent 后端。

        【触发场景】：
        - "重新开始"、"清空重来"、"换个话题"
        - "换成本机的 claude"、"用本地 agent 试试"

        【后端说明】：
        - llm：由 oai-agent-apiserver 提供服务（默认，历史保存在服务端）
        - cli：在本机的一个独立子目录里运行 claude（需管理员开启，仅管理员可用）

        /new 会清空当前会话的记忆；只想换个地方继续、不想清空上下文时用 /switch。`)

	newExample = templates.Examples(`
        # 重新开始一个默认后端的对话
        /new

        # 在本机 CLI 后端上重新开始
        /new --backend cli

        # 保留上下文，只把后续对话切到本机 CLI 后端
        /switch cli`)
)

// NewNewCmd creates the "new" command.
func NewNewCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &bindingOptions{out: out, clear: true}

	cmd := &cobra.Command{
		Use:                   "new",
		Aliases:               []string{"reset"},
		Short:                 "开始新对话（清空上下文，可指定 Agent 后端）",
		Long:                  newLong,
		Example:               newExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Annotations: map[string]string{
			chat.ChatGroup: chat.ChatGroupAI,
			chat.AdminOnly: chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd.Context(), args); err != nil {
				return err
			}
			if err := o.Validate(cmd.Context(), sr, args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), sr, runtime.From(cmd.Context()), args)
		},
	}

	cmd.Flags().StringVarP(&o.Backend, "backend", "b", "",
		"Agent 后端：llm（服务端）或 cli（本机子进程）；留空表示当前/默认后端")

	return cmd
}

// NewSwitchCmd creates the "switch" command.
//
// It is /new's sibling rather than its alias: /new forgets the conversation, and
// /switch deliberately does not. What /switch changes is where the next turn
// runs, so a conversation moved to the CLI backend and back still finds its
// agent history.
//
// # It is hidden
//
// `switch` names two different things on two surfaces. The card's "切换 Agent" (switch Agent)
// button carries the action string `nav:/switch` / `act:/switch <name>`, which
// swaps the conversation's *agent*; this command swaps the *backend* (llm/cli).
// The card action never reaches the command tree (cardnav.go matches it first),
// so the collision is one of wording, not of behaviour — but a reader who has
// seen the button reasonably expects `/switch foo` to pick an agent, and gets
// "后端只能是 llm 或 cli" (the backend can only be llm or cli) instead.
//
// Hiding it rather than renaming or deleting it: the action strings are baked
// into cards already sitting in chat, so the wording cannot be reclaimed
// cheaply; and a conversation parked on the cli backend needs a way back that
// does not forget its history, which /new --backend cannot do. A hidden command
// is still dispatchable — whoever has used it keeps it — it is simply absent
// from /help, the TUI command tree, and the help card, all three of which skip
// Hidden (onexstack's cmdGroupsString, bot/command.go, cardnav.go).
func NewSwitchCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &bindingOptions{out: out, clear: false}

	cmd := &cobra.Command{
		Use:     "switch <backend>",
		Aliases: []string{"backend"},
		Short:   "切换当前对话的 Agent 后端（保留上下文）",
		Long: templates.LongDesc(`
        把当前对话切换到另一个 Agent 后端，不清理上下文。

        与 /new 的区别：/new 会忘记当前会话，/switch 只是换个地方继续。
        agent 侧的历史会保留，切回去还能接上；cli 侧的会话属于它自己，
        切换过去时会在本机开始一个新会话。`),
		Example:               templates.Examples("\n        /switch cli\n        /switch llm"),
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		// Hidden keeps the command out of every menu; it is still callable by
		// name. See the doc comment for why the wording cannot be reclaimed.
		Hidden: true,
		Annotations: map[string]string{
			chat.ChatGroup: chat.ChatGroupAI,
			chat.AdminOnly: chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd.Context(), args); err != nil {
				return err
			}
			if err := o.Validate(cmd.Context(), sr, args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), sr, runtime.From(cmd.Context()), args)
		},
	}

	return cmd
}

// bindingOptions is what /new and /switch share: which backend a conversation
// should run on, and whether moving it also forgets it.
type bindingOptions struct {
	out runtime.Responder
	// Backend is the requested backend: --backend for /new, the positional
	// argument for /switch. Empty means "wherever this conversation already is".
	Backend string
	// clear is true for /new: the conversation is abandoned rather than moved, so
	// its history goes with it.
	clear bool
}

// Complete captures the backend for /switch.
func (o *bindingOptions) Complete(_ context.Context, args []string) error {
	if len(args) > 0 {
		o.Backend = args[0]
	}
	return nil
}

// Validate checks the requested name.
//
// The check is here rather than in Run so the user is told what they could have
// asked for: a conversation bound to a backend nobody registered would fail on
// every turn afterwards, and the failure would look like an agent that stopped
// answering. It takes the runtime, unlike its siblings in this tree, only so the
// rejection is written in the deployment's language — the name being rejected is
// not something the runtime knows.
func (o *bindingOptions) Validate(_ context.Context, sr *runtime.StaticRuntime, _ []string) error {
	o.Backend = strings.ToLower(strings.TrimSpace(o.Backend))
	if o.Backend == "" {
		return nil
	}
	if o.Backend != agent.BackendLLM && o.Backend != agent.BackendCLI {
		return sr.UserError("后端只能是 llm 或 cli，收到：%s", o.Backend)
	}
	return nil
}

// Run rebinds the conversation and reports what it is now bound to.
func (o *bindingOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime, _ []string) error {
	if sr == nil || sr.Sessions == nil {
		return sr.UserError("本服务未启用会话管理，无法切换后端")
	}
	if sr.Agents == nil {
		return sr.UserError("本服务未配置 Agent 后端")
	}
	key := conversationKey(dr)
	if key == "" {
		return sr.UserError("无法确定当前会话，请在具体的聊天中使用该命令")
	}

	if requested := o.Backend; requested != "" {
		if _, ok := sr.Agents.Get(requested); !ok {
			return sr.UserError("该后端未启用：%s。可用：%s",
				requested, strings.Join(o.available(sr), "、"))
		}
	}

	sess, err := sr.Sessions.Session(key)
	if err != nil {
		return err
	}
	current := sess.State().Backend
	target := agent.ResolveBackend(o.Backend, current, sr.Agents.Fallback())
	moved := target != current

	// Nothing to do: the caller named the backend the conversation is already on
	// and is not asking to forget anything. Reported rather than rewritten, so a
	// /switch that changes nothing leaves no trace in the snapshot.
	if !moved && !o.clear {
		return o.report(ctx, sr, sess, target, false)
	}

	if err := o.rebind(ctx, sr, sess, target); err != nil {
		return err
	}
	return o.report(ctx, sr, sess, target, moved)
}

// rebind moves the conversation onto target.
//
// The old backend is released first: a conversation that was running a process
// on this host must not keep it alive just because the chat moved on. The
// recorded resume id is cleared with it, because a session id only means
// something to the backend that issued it — handing the CLI's id to agent, or
// the reverse, would resume a conversation that never existed there.
func (o *bindingOptions) rebind(ctx context.Context, sr *runtime.StaticRuntime, sess *session.Session, target string) error {
	if err := sr.Agents.CloseSession(ctx, sess.Key()); err != nil {
		// Worth a warning and not a failure: the conversation is moving anyway, and
		// a process that outlives its binding is reaped with the conversation.
		slog.WarnContext(ctx, "failed to release the agent session of a rebound conversation",
			"conv_id", sess.Key(), "error", err)
	}

	if o.clear {
		// The agent side owns the history for the llm backend, so forgetting the
		// conversation means asking it to forget too. Best-effort: the binding is
		// what this command is about, and a conversation whose history outlived the
		// reset is a stale answer rather than a broken command.
		o.clearHistory(ctx, sr, sess.Key())
	}

	// Only the binding is rewritten. The working directory set with /dir survives
	// both commands, for the same reason Mode does: it says where this
	// conversation's work lives, not what one turn was about. A user who pointed a
	// conversation at a checkout and then said /new wants a fresh conversation in
	// that checkout, not a silent move back to an empty directory; /dir --default
	// is the way to drop it.
	return sess.Update(func(st *session.State) {
		st.Backend = target
		st.BackendSessionID = ""
	})
}

// clearHistory asks oai-agent-apiserver to forget the conversation.
func (o *bindingOptions) clearHistory(ctx context.Context, sr *runtime.StaticRuntime, key string) {
	if sr.Clientset == nil || sr.Clientset.Agent() == nil {
		return
	}
	if err := sr.Clientset.Agent().ClearSession(ctx, key); err != nil {
		slog.WarnContext(ctx, "failed to clear the agent conversation on /new",
			"conv_id", key, "error", err)
	}
}

// report tells the user where the conversation now stands.
func (o *bindingOptions) report(ctx context.Context, sr *runtime.StaticRuntime, sess *session.Session, target string, moved bool) error {
	state := sess.State()

	var b strings.Builder
	if moved {
		b.WriteString(sr.Translate("已切换到 **%s** 后端。", target))
	} else {
		b.WriteString(sr.Translate("当前后端：**%s**。", target))
	}
	if target == agent.BackendCLI {
		b.WriteString("\n")
		b.WriteString(sr.Translate("本机 CLI 会话在自己的目录里运行，工具调用需要逐次批准。"))
	} else {
		b.WriteString("\n")
		b.WriteString(sr.Translate("由 oai-agent-apiserver 提供服务。"))
	}
	switch {
	case o.clear:
		b.WriteString("\n")
		b.WriteString(sr.Translate("上下文已清空，这是一个新的对话。"))
	case state.BackendSessionID != "":
		b.WriteString("\n")
		b.WriteString(sr.Translate("上下文保留，可以继续之前的对话。"))
	default:
		b.WriteString("\n")
		b.WriteString(sr.Translate("上下文保存在对应的后端上。"))
	}

	return o.out.SendResult(ctx, &runtime.CommandResult{PlainText: b.String()})
}

// available lists the backends this deployment registered, for the error message
// that has to say what the user could have asked for instead.
func (o *bindingOptions) available(sr *runtime.StaticRuntime) []string {
	names := sr.Agents.Names()
	sort.Strings(names)
	if len(names) == 0 {
		return []string{agent.BackendLLM}
	}
	return names
}

// conversationKey is the conversation a command is running in.
//
// It has to be the key an AI turn runs under — dr.Address(), which for a Lark
// message is the group chat or the sender's private conversation — because a
// binding written under any other key is a binding the next turn never reads.
// A caller that has only a conversation id (a REST command) falls back to it, so
// /new names the same conversation the turn after it will.
func conversationKey(dr *runtime.DynamicRuntime) string {
	if dr == nil {
		return ""
	}
	if dr.Receiver != nil && dr.Address() != "" {
		return dr.Address()
	}
	return dr.ConvID
}
