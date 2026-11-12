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
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	"github.com/onexstack/onexai/pkg/agentcli"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// scopeBackend is the CLI backend as /dir sees it: a backend that can say which
// directories its turns may run in. The plain stub cannot, which is one of the
// cases below rather than an oversight — a backend that holds no directory is a
// different refusal from a backend that is switched off.
type scopeBackend struct {
	*stubBackend
	roots []string
}

func (s *scopeBackend) AllowedRoots() []string { return s.roots }

// dirFixture is a deployment whose CLI backend can report its allowed roots.
type dirFixture struct {
	*bindingFixture
	root  string
	scope *scopeBackend
}

func newDirFixture(t *testing.T) *dirFixture {
	t.Helper()

	f := newBindingFixture(t)
	root := t.TempDir()
	scope := &scopeBackend{stubBackend: f.cli, roots: []string{root}}
	f.sr.Agents = agent.NewRegistry(agent.BackendLLM, f.llm, scope)

	return &dirFixture{bindingFixture: f, root: root, scope: scope}
}

// run executes /dir the way the command does: through the options, so the test
// covers the same path a user's message takes.
func (f *dirFixture) run(t *testing.T, path string, def bool) error {
	t.Helper()
	o := &dirOptions{out: f.reply, Path: path, Default: def}
	return o.Run(context.Background(), f.sr, f.dr)
}

// ownWorkspace is the directory this conversation runs in when it has asked for
// nothing, which is the answer /dir has to give before anything has been set.
func (f *dirFixture) ownWorkspace(t *testing.T) string {
	t.Helper()
	own, err := agentcli.SessionWorkspace(f.root, "oc_chat_1")
	if err != nil {
		t.Fatalf("SessionWorkspace: %v", err)
	}
	return own
}

// TestDirReportsTheSessionsOwnWorkspace pins the default answer. A conversation
// that has never used /dir still has a working directory — its own — and a
// command that said "not set" would leave the user unable to tell whether the
// agent was running somewhere or nowhere.
func TestDirReportsTheSessionsOwnWorkspace(t *testing.T) {
	f := newDirFixture(t)

	if err := f.run(t, "", false); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	reply := f.reply.text()
	if own := f.ownWorkspace(t); !strings.Contains(reply, own) {
		t.Errorf("reply = %q, want the session's own workspace %q", reply, own)
	}
	if !strings.Contains(reply, "（会话自带）") {
		t.Errorf("reply = %q, want it to say the directory is the session's own", reply)
	}
	if !strings.Contains(reply, f.root) {
		t.Errorf("reply = %q, want the allowed roots listed", reply)
	}
	// Reporting is not setting: a conversation that has asked for nothing must
	// still have asked for nothing afterwards.
	if got := f.state(t).WorkDir; got != "" {
		t.Errorf("WorkDir = %q after a report, want it unset", got)
	}
}

// TestDirPointsTheConversationAtAnAllowedDirectory is the command's purpose: the
// setting lands in the conversation's snapshot, which is what survives a restart
// and what the next turn reads.
func TestDirPointsTheConversationAtAnAllowedDirectory(t *testing.T) {
	f := newDirFixture(t)
	target := filepath.Join(f.root, "checkout")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if err := f.run(t, target, false); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got := f.state(t).WorkDir; got != target {
		t.Errorf("WorkDir = %q, want %q", got, target)
	}
	if reply := f.reply.text(); !strings.Contains(reply, target) {
		t.Errorf("reply = %q, want it to name the directory that was set", reply)
	}
	// The conversation is still on the llm backend, so the setting cannot take
	// effect yet. Saying so is the difference between a command that worked and a
	// command whose effect the user will never see.
	if reply := f.reply.text(); !strings.Contains(reply, agent.BackendLLM) {
		t.Errorf("reply = %q, want it to say the backend is not the CLI one", reply)
	}
}

// TestDirResolvesARelativePathAgainstTheSessionWorkspace pins what "src" means
// when it arrives from a chat. It is the session's own directory, not the
// server's working directory: the second is not a place the user can see, and a
// path resolved against it would be a directory nobody asked for.
func TestDirResolvesARelativePathAgainstTheSessionWorkspace(t *testing.T) {
	f := newDirFixture(t)
	want := filepath.Join(f.ownWorkspace(t), "src")
	if err := os.Mkdir(want, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if err := f.run(t, "src", false); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got := f.state(t).WorkDir; got != want {
		t.Errorf("WorkDir = %q, want %q", got, want)
	}
}

// TestDirRejectsAPathItCannotUse pins the confinement, and that a refusal writes
// nothing: a conversation left pointing at a directory the backend would refuse
// on the next turn is a conversation that fails when the user is not looking.
func TestDirRejectsAPathItCannotUse(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(t *testing.T, f *dirFixture) string
	}{
		{
			name: "a directory outside the allowed roots",
			path: func(t *testing.T, _ *dirFixture) string { return t.TempDir() },
		},
		{
			name: "a directory under a root that does not exist",
			path: func(_ *testing.T, f *dirFixture) string { return filepath.Join(f.root, "gone") },
		},
		{
			name: "a file rather than a directory",
			path: func(t *testing.T, f *dirFixture) string {
				file := filepath.Join(f.root, "a.txt")
				if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return file
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDirFixture(t)
			path := tc.path(t, f)

			err := f.run(t, path, false)
			if err == nil {
				t.Fatal("Run() accepted a directory it cannot use")
			}
			if !runtime.IsUserError(err) {
				t.Errorf("Run() = %v, want a user-facing error", err)
			}
			if got := f.state(t).WorkDir; got != "" {
				t.Errorf("WorkDir = %q after a refusal, want the conversation untouched", got)
			}
		})
	}
}

// TestDirDefaultGoesBackToTheSessionWorkspace pins the way out. An override that
// could not be undone would leave a conversation stuck in a directory the user
// picked once, which for a conversation that outlives its purpose is a leak of
// access as much as of state.
func TestDirDefaultGoesBackToTheSessionWorkspace(t *testing.T) {
	f := newDirFixture(t)
	f.bind(t, session.State{
		Key:     "oc_chat_1",
		Backend: agent.BackendCLI,
		WorkDir: filepath.Join(f.root, "checkout"),
	})

	if err := f.run(t, "", true); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got := f.state(t).WorkDir; got != "" {
		t.Errorf("WorkDir = %q after --default, want it cleared", got)
	}
	if reply := f.reply.text(); !strings.Contains(reply, "已恢复") {
		t.Errorf("reply = %q, want it to say the override was dropped", reply)
	}
}

// TestDirRefusesADefaultWithAPath pins the argument pair that would otherwise
// have to be resolved by silently preferring one of them. The user who typed both
// believes both took effect, and one of them did not.
func TestDirRefusesADefaultWithAPath(t *testing.T) {
	f := newDirFixture(t)
	target := filepath.Join(f.root, "checkout")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	err := f.run(t, target, true)
	if err == nil {
		t.Fatal("Run() accepted --default together with a path")
	}
	if !runtime.IsUserError(err) {
		t.Errorf("Run() = %v, want a user-facing error", err)
	}
	if got := f.state(t).WorkDir; got != "" {
		t.Errorf("WorkDir = %q, want the conversation untouched", got)
	}
}

// TestDirRefusesWhenNoBackendCanAnswer pins the three refusals that look the
// same from a chat and have different fixes: the service has no backends, the
// CLI backend is off, or the backend it found cannot say where it runs. Each
// has to name itself, or the user's only recourse is to guess which of the three
// is true.
func TestDirRefusesWhenNoBackendCanAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		set  func(f *dirFixture)
	}{
		{
			name: "no backends registered at all",
			want: "本服务未配置 Agent 后端",
			set:  func(f *dirFixture) { f.sr.Agents = nil },
		},
		{
			name: "the cli backend is not registered",
			want: "本机 CLI 后端未启用",
			set:  func(f *dirFixture) { f.sr.Agents = agent.NewRegistry(agent.BackendLLM, f.llm) },
		},
		{
			name: "the cli backend holds no directory",
			want: "不支持指定工作目录",
			set:  func(f *dirFixture) { f.sr.Agents = agent.NewRegistry(agent.BackendLLM, f.llm, f.cli) },
		},
		{
			name: "the backend allows nothing",
			want: "未配置工作区根目录",
			set:  func(f *dirFixture) { f.scope.roots = nil },
		},
		{
			name: "the service has no sessions",
			want: "本服务未启用会话管理",
			set:  func(f *dirFixture) { f.sr.Sessions = nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDirFixture(t)
			tc.set(f)

			err := f.run(t, "", false)
			if err == nil {
				t.Fatal("Run() answered a caller with no backend to answer for")
			}
			if !runtime.IsUserError(err) {
				t.Errorf("Run() = %v, want a user-facing error", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// TestDirMarksARootThatIsNotThere pins how a mistyped allowed directory shows up.
// The list is configuration, and configuration is wrong sometimes: a root that
// does not exist has to be visible as configured-but-missing, which is a
// different problem from not being configured at all.
func TestDirMarksARootThatIsNotThere(t *testing.T) {
	f := newDirFixture(t)
	missing := filepath.Join(f.root, "not-created")
	f.scope.roots = []string{f.root, missing}

	if err := f.run(t, "", false); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	reply := f.reply.text()
	if !strings.Contains(reply, missing) {
		t.Errorf("reply = %q, want the missing root %q named", reply, missing)
	}
	if !strings.Contains(reply, "（不存在）") {
		t.Errorf("reply = %q, want the missing root marked as missing", reply)
	}
	// With more than one root the reply has to say that the roots are not the
	// whole story, or the user reads the list as the set of usable directories.
	if !strings.Contains(reply, "根目录下的任意已存在目录") {
		t.Errorf("reply = %q, want it to explain that subdirectories may be used", reply)
	}
}

// TestDirIsAdminOnly pins the gate. This command decides which directory a
// process on this host runs in, which is the same kind of decision as
// /new --backend=cli and carries the same annotation.
func TestDirIsAdminOnly(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})
	cmd := NewDirCmd(&runtime.StaticRuntime{}, out)

	if cmd.Annotations[chat.AdminOnly] != chat.True {
		t.Errorf("/dir is not annotated %s", chat.AdminOnly)
	}
	if cmd.Annotations[chat.ChatGroup] != chat.ChatGroupAI {
		t.Errorf("/dir is not grouped with the AI commands")
	}

	// It takes at most one path, and a second argument is a usage error rather
	// than something quietly ignored: "/dir a b" is a user who believes two
	// directories were named.
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Error("/dir accepted two paths")
	}
	if err := cmd.Args(cmd, nil); err != nil {
		t.Errorf("/dir with no path = %v, want the report form to be valid", err)
	}
}

// TestDirReportsTheRequestedDirectoryAsTheEffectiveOne pins that a conversation
// which has been pointed somewhere says so, and says how to get back. The two
// pieces together are what stop a user from believing the override is the
// default and wondering later why a new conversation starts elsewhere.
func TestDirReportsTheRequestedDirectoryAsTheEffectiveOne(t *testing.T) {
	f := newDirFixture(t)
	target := filepath.Join(f.root, "checkout")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	f.bind(t, session.State{Key: "oc_chat_1", Backend: agent.BackendCLI, WorkDir: target})

	if err := f.run(t, "", false); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	reply := f.reply.text()
	if !strings.Contains(reply, target) || !strings.Contains(reply, "已指定") {
		t.Errorf("reply = %q, want the requested directory reported as the effective one", reply)
	}
	if own := f.ownWorkspace(t); !strings.Contains(reply, own) {
		t.Errorf("reply = %q, want the session's own workspace %q named as the alternative", reply, own)
	}
	if !strings.Contains(reply, "/dir --default") {
		t.Errorf("reply = %q, want it to say how to go back", reply)
	}
}
