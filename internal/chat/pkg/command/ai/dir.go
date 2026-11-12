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
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	"github.com/onexstack/onexai/pkg/agentcli"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	dirLong = templates.LongDesc(`
        查看或指定本对话的本机 CLI 会话在哪个目录里运行。

        【触发场景】：
        - "你现在在哪个目录"、"去 /data/code/xxx 看看"
        - 把对话指向一个已经存在的代码仓库，而不是会话自带的空目录

        【默认行为】：
        每个对话在自己的独立子目录里运行（工作区根目录下的一个子目录），
        互不干扰。不带参数时报告当前目录。

        【边界】：
        只能指向工作区根目录或其允许目录之内的已存在目录。这是本地进程的
        运行目录，越界的路径会被拒绝而不是就地创建。`)

	dirExample = templates.Examples(`
        # 看看这个对话在哪个目录里运行
        /dir

        # 把该对话指向一个已存在的仓库
        /dir /data/code/onexai

        # 回到会话自带的目录
        /dir --default`)
)

// NewDirCmd creates the "dir" command.
func NewDirCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &dirOptions{out: out}

	cmd := &cobra.Command{
		Use:                   "dir [path]",
		Aliases:               []string{"cd", "workdir"},
		Short:                 "查看或指定本对话的工作目录（仅管理员）",
		Long:                  dirLong,
		Example:               dirExample,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Args:                  cobra.MaximumNArgs(1),
		Annotations: map[string]string{
			chat.ChatGroup: chat.ChatGroupAI,
			// It is the directory a local process will run in, which is the same
			// kind of decision as /new --backend=cli, and it carries the same gate.
			chat.AdminOnly: chat.True,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			o.Path = ""
			if len(args) > 0 {
				o.Path = args[0]
			}
			return o.Run(cmd.Context(), sr, runtime.From(cmd.Context()))
		},
	}

	cmd.Flags().BoolVar(&o.Default, "default", false,
		"Drop the override and go back to this conversation's own workspace directory")

	return cmd
}

// dirOptions is what /dir needs: somewhere to answer, the requested path, and
// whether the caller is asking for the default instead.
type dirOptions struct {
	out     runtime.Responder
	Path    string
	Default bool
}

// Run reports or sets the conversation's working directory.
func (o *dirOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime) error {
	// Asked for both at once, the command would silently honour one of them. The
	// pair is refused instead, because a user who typed "/dir --default /data/x"
	// believes the path took effect.
	if o.Default && o.Path != "" {
		return sr.UserError("--default 与路径只能给出一个")
	}
	if sr == nil || sr.Sessions == nil {
		return sr.UserError("本服务未启用会话管理，无法指定工作目录")
	}
	key := conversationKey(dr)
	if key == "" {
		return sr.UserError("无法确定当前会话，请在具体的聊天中使用该命令")
	}

	// The scope comes from the backend rather than from a copy of the
	// configuration here: the backend is what will enforce it when the process
	// starts, and two lists that can disagree is one list too many.
	scope, err := dirScope(sr)
	if err != nil {
		return err
	}
	roots := scope.AllowedRoots()
	if len(roots) == 0 {
		// Unreachable through the configuration — a CLI backend without a
		// workspace root is refused at startup — but this command indexes the
		// first root, and a panic in a chat command is not an acceptable way to
		// find out that two things have stopped agreeing.
		return sr.UserError("本机 CLI 后端未配置工作区根目录，无法指定工作目录")
	}

	sess, err := sr.Sessions.Session(key)
	if err != nil {
		return err
	}

	switch {
	case o.Default:
		return o.set(ctx, sr, sess, "")
	case o.Path != "":
		// A relative path is resolved against the conversation's own workspace, not
		// against the server's working directory: "/dir src" from a chat means
		// "src here", and the server's cwd is not a directory the user can see.
		path := o.Path
		if !filepath.IsAbs(path) {
			own, err := agentcli.SessionWorkspace(roots[0], key)
			if err != nil {
				return err
			}
			path = filepath.Join(own, path)
		}
		// Resolved through the same function the backend uses, so a path the
		// command accepts is a path the turn will accept. A check that only
		// resembled the backend's would be a check that let something through
		// here and failed after the user had been told it worked.
		abs, err := agentcli.ResolveDir(roots, path)
		if err != nil {
			return sr.UserError("无法使用该目录：%v。允许的目录：%s", err, strings.Join(roots, "、"))
		}
		return o.set(ctx, sr, sess, abs)
	default:
		return o.report(ctx, sr, sess, roots, key)
	}
}

// set writes the conversation's working directory. An empty dir clears it, which
// is what puts the conversation back on its own workspace.
func (o *dirOptions) set(ctx context.Context, sr *runtime.StaticRuntime, sess *session.Session, dir string) error {
	if err := sess.Update(func(st *session.State) { st.WorkDir = dir }); err != nil {
		return err
	}

	var b strings.Builder
	if dir == "" {
		b.WriteString(sr.Translate("已恢复为会话自带的目录。"))
	} else {
		b.WriteString(sr.Translate("已把本对话的工作目录设为 **%s**。", dir))
	}
	if backend := sess.State().Backend; backend != agent.BackendCLI {
		// Said rather than refused: the setting is recorded either way, and
		// refusing it would order two commands that have no reason to be ordered.
		b.WriteString("\n")
		b.WriteString(sr.Translate("当前后端是 %s，本机 CLI 会话建立后生效。", backend))
	}
	return o.out.SendResult(ctx, &runtime.CommandResult{PlainText: b.String()})
}

// report describes where this conversation's turns run.
func (o *dirOptions) report(ctx context.Context, sr *runtime.StaticRuntime, sess *session.Session, roots []string, key string) error {
	state := sess.State()
	own, err := agentcli.SessionWorkspace(roots[0], key)
	if err != nil {
		return err
	}

	var b strings.Builder
	if state.WorkDir != "" {
		b.WriteString(sr.Translate("本对话的工作目录：**%s**（已指定）\n", state.WorkDir))
		b.WriteString(sr.Translate("会话自带的目录是 %s，用 /dir --default 可以回去。", own))
	} else {
		b.WriteString(sr.Translate("本对话的工作目录：**%s**（会话自带）", own))
	}

	b.WriteString("\n")
	b.WriteString(sr.Translate("可用的目录：%s", strings.Join(shortRoots(roots), "、")))
	if len(roots) > 1 {
		b.WriteString("\n")
		b.WriteString(sr.Translate("（只列出根目录，根目录下的任意已存在目录都可以用）"))
	}

	return o.out.SendResult(ctx, &runtime.CommandResult{PlainText: b.String()})
}

// dirScope finds the backend that can say which directories are allowed.
//
// The refusal names what to do about it, because the two reasons this fails —
// the backend is off, or the backend holds no directory — look identical from
// the user's side and have different fixes.
func dirScope(sr *runtime.StaticRuntime) (agent.DirScope, error) {
	if sr.Agents == nil {
		return nil, sr.UserError("本服务未配置 Agent 后端")
	}
	backend, ok := sr.Agents.Get(agent.BackendCLI)
	if !ok {
		return nil, sr.UserError("本机 CLI 后端未启用，无法指定工作目录")
	}
	scope, ok := backend.(agent.DirScope)
	if !ok {
		return nil, sr.UserError("当前 Agent 后端不支持指定工作目录")
	}
	return scope, nil
}

// shortRoots renders the allowed roots for a reply. A root that cannot be
// resolved is shown as it was configured rather than dropped: a user whose
// allowed directory is missing from the list needs to see that it is configured,
// which is a different problem from it not being configured at all.
func shortRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			out = append(out, root+"（不存在）")
			continue
		}
		out = append(out, root)
	}
	sort.Strings(out)
	return out
}
