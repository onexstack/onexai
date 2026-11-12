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
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// stubBackend is a backend that records what was asked of it. Nothing here runs
// a turn: what /new and /switch decide is which backend a conversation is bound
// to, and the only effect on a backend is that the old one is told to let go.
type stubBackend struct {
	name string

	mu     sync.Mutex
	closed []string
}

func (s *stubBackend) Name() string { return s.name }

func (s *stubBackend) Session(context.Context, string) (agent.Session, error) { return nil, nil }

func (s *stubBackend) CloseSession(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = append(s.closed, key)
	return nil
}

func (s *stubBackend) Close() error { return nil }

// released reports the keys this backend was asked to let go of.
func (s *stubBackend) released() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.closed...)
}

// bindingFixture is one deployment's worth of state: the conversations a
// command rebinds, and the backends it rebinds them between.
type bindingFixture struct {
	sr    *runtime.StaticRuntime
	dr    *runtime.DynamicRuntime
	reply *runtimeCommandResult
	llm   *stubBackend
	cli   *stubBackend
}

// newBindingFixture builds a runtime with a manager over a temporary directory
// and both backends registered.
func newBindingFixture(t *testing.T) *bindingFixture {
	t.Helper()

	llm := &stubBackend{name: agent.BackendLLM}
	cli := &stubBackend{name: agent.BackendCLI}

	mgr := session.NewManager(context.Background(), session.Options{
		DataDir:        t.TempDir(),
		DefaultBackend: agent.BackendLLM,
	})

	return &bindingFixture{
		sr: &runtime.StaticRuntime{
			Sessions: mgr,
			Agents:   agent.NewRegistry(agent.BackendLLM, llm, cli),
		},
		dr:    &runtime.DynamicRuntime{ConvID: "oc_chat_1", IsAdmin: true},
		reply: &runtimeCommandResult{},
		llm:   llm,
		cli:   cli,
	}
}

// bindKey gives the conversation under key a starting state, as if it had been
// used before.
func (f *bindingFixture) bindKey(t *testing.T, key string, state session.State) {
	t.Helper()

	sess, err := f.sr.Sessions.Session(key)
	if err != nil {
		t.Fatalf("Session(%q) = %v", key, err)
	}
	if err := sess.Update(func(st *session.State) {
		*st = state
		// The snapshot is keyed by the conversation it describes; a state written
		// without its key is refused by the store, so the fixture fills it in
		// rather than making every case restate it.
		if st.Key == "" {
			st.Key = key
		}
	}); err != nil {
		t.Fatalf("Update() = %v", err)
	}
}

// bind is bindKey for the conversation the fixture's commands run in.
func (f *bindingFixture) bind(t *testing.T, state session.State) {
	t.Helper()
	f.bindKey(t, "oc_chat_1", state)
}

// state reads the conversation's binding back.
func (f *bindingFixture) state(t *testing.T) session.State {
	t.Helper()

	sess, err := f.sr.Sessions.Session("oc_chat_1")
	if err != nil {
		t.Fatalf("Session() = %v", err)
	}
	return sess.State()
}

// TestNewBindsConversationToCLI is the command half of the CLI backend's front
// door: /new --backend=cli has to leave the conversation bound to cli, told the
// old backend to let go, and said so to the user.
func TestNewBindsConversationToCLI(t *testing.T) {
	f := newBindingFixture(t)
	f.bind(t, session.State{Key: "oc_chat_1", Backend: agent.BackendLLM})

	o := &bindingOptions{out: f.reply, clear: true, Backend: agent.BackendCLI}
	if err := o.Validate(context.Background(), &runtime.StaticRuntime{}, nil); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if err := o.Run(context.Background(), f.sr, f.dr, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got := f.state(t).Backend; got != agent.BackendCLI {
		t.Errorf("Backend = %q, want %q", got, agent.BackendCLI)
	}
	// The llm backend held the conversation before this command. A process that
	// outlived its binding is the failure this guards.
	if released := f.llm.released(); len(released) != 1 || released[0] != "oc_chat_1" {
		t.Errorf("llm backend released %v, want [oc_chat_1]", released)
	}
	if !strings.Contains(f.reply.text(), agent.BackendCLI) {
		t.Errorf("reply = %q, want it to name the new backend", f.reply.text())
	}
}

// TestNewClearsTheResumeID is the case that makes /new more than a rename of
// /switch: a conversation reset on the backend it is already on must still lose
// its resume id and its agent history. Skipping the write because the backend
// did not change is exactly the bug this covers.
func TestNewClearsTheResumeID(t *testing.T) {
	f := newBindingFixture(t)
	f.bind(t, session.State{Key: "oc_chat_1", Backend: agent.BackendCLI, BackendSessionID: "sess-cli-old"})

	o := &bindingOptions{out: f.reply, clear: true}
	if err := o.Run(context.Background(), f.sr, f.dr, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	state := f.state(t)
	if state.Backend != agent.BackendCLI {
		t.Errorf("Backend = %q, want the conversation to stay on cli", state.Backend)
	}
	if state.BackendSessionID != "" {
		t.Errorf("BackendSessionID = %q, want it cleared: the reset conversation is not %q",
			state.BackendSessionID, "sess-cli-old")
	}
}

// TestNewKeepsTheWorkingDirectory pins the line /new draws: it forgets the
// conversation and not the setting around it. A user who pointed a conversation
// at a checkout and then started over wants a fresh conversation there, not a
// silent move back to an empty directory — and the way to drop it is /dir
// --default, which says so.
func TestNewKeepsTheWorkingDirectory(t *testing.T) {
	f := newBindingFixture(t)
	dir := t.TempDir()
	f.bind(t, session.State{Key: "oc_chat_1", Backend: agent.BackendCLI, WorkDir: dir})

	o := &bindingOptions{out: f.reply, clear: true}
	if err := o.Run(context.Background(), f.sr, f.dr, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got := f.state(t).WorkDir; got != dir {
		t.Errorf("WorkDir = %q after /new, want it kept as %q", got, dir)
	}
}

// TestSwitchKeepsTheContextAndDropsTheForeignResumeID pins both halves of the
// difference between /switch and /new. The agent history survives, so the
// reply says the context was kept; the CLI session id does not, because an id
// only means something to the backend that issued it.
func TestSwitchKeepsTheContextAndDropsTheForeignResumeID(t *testing.T) {
	f := newBindingFixture(t)
	f.bind(t, session.State{Key: "oc_chat_1", Backend: agent.BackendCLI, BackendSessionID: "sess-cli-old"})

	o := &bindingOptions{out: f.reply, clear: false, Backend: agent.BackendLLM}
	if err := o.Run(context.Background(), f.sr, f.dr, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	state := f.state(t)
	if state.Backend != agent.BackendLLM {
		t.Errorf("Backend = %q, want %q", state.Backend, agent.BackendLLM)
	}
	if state.BackendSessionID != "" {
		t.Errorf("BackendSessionID = %q, want the CLI id dropped rather than handed to agent",
			state.BackendSessionID)
	}
	if released := f.cli.released(); len(released) != 1 {
		t.Errorf("cli backend released %v, want the conversation", released)
	}
}

// TestSwitchToTheCurrentBackendIsANoop pins that a /switch that changes nothing
// writes nothing. Rebinding a conversation to where it already is would drop its
// resume id and tell a live backend to let go of a conversation that is still
// running on it.
func TestSwitchToTheCurrentBackendIsANoop(t *testing.T) {
	f := newBindingFixture(t)
	f.bind(t, session.State{Key: "oc_chat_1", Backend: agent.BackendCLI, BackendSessionID: "sess-cli-live"})

	o := &bindingOptions{out: f.reply, clear: false, Backend: agent.BackendCLI}
	if err := o.Run(context.Background(), f.sr, f.dr, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	state := f.state(t)
	if state.BackendSessionID != "sess-cli-live" {
		t.Errorf("BackendSessionID = %q, want the running session left alone", state.BackendSessionID)
	}
	if released := f.cli.released(); len(released) != 0 {
		t.Errorf("cli backend was told to release %v, want nothing", released)
	}
	if !strings.Contains(f.reply.text(), agent.BackendCLI) {
		t.Errorf("reply = %q, want it to report the current backend", f.reply.text())
	}
}

// TestUnregisteredBackendIsRefused pins the failure the user sees when the
// deployment does not have the backend they asked for: an error naming what does
// exist, and a conversation left exactly where it was.
func TestUnregisteredBackendIsRefused(t *testing.T) {
	f := newBindingFixture(t)
	// A deployment with the CLI backend disabled: the registry the provider builds
	// then holds llm alone.
	f.sr.Agents = agent.NewRegistry(agent.BackendLLM, f.llm)
	f.bind(t, session.State{Key: "oc_chat_1", Backend: agent.BackendLLM})

	o := &bindingOptions{out: f.reply, clear: true, Backend: agent.BackendCLI}
	err := o.Run(context.Background(), f.sr, f.dr, nil)
	if err == nil {
		t.Fatal("Run() bound a conversation to a backend that is not registered")
	}
	if !runtime.IsUserError(err) {
		t.Errorf("Run() = %v, want a user-facing error", err)
	}
	if !strings.Contains(err.Error(), agent.BackendCLI) || !strings.Contains(err.Error(), agent.BackendLLM) {
		t.Errorf("error = %v, want it to name both the refused backend and the available one", err)
	}
	if got := f.state(t).Backend; got != agent.BackendLLM {
		t.Errorf("Backend = %q, want the conversation untouched", got)
	}
}

// TestValidateRejectsUnknownBackendName pins the check that runs before anything
// is written. A conversation bound to a name nobody registered would fail on
// every turn afterwards, and the failure would look like an agent that went
// silent rather than like a typo.
func TestValidateRejectsUnknownBackendName(t *testing.T) {
	o := &bindingOptions{Backend: "  GPT-From-2019 "}
	err := o.Validate(context.Background(), &runtime.StaticRuntime{}, nil)
	if err == nil {
		t.Fatal("Validate() accepted an unknown backend name")
	}
	if !strings.Contains(err.Error(), "gpt-from-2019") {
		t.Errorf("error = %v, want it to echo the name that was refused", err)
	}

	// The accepted names are case- and space-insensitive, so a command typed by a
	// human puts the canonical value in the snapshot.
	o = &bindingOptions{Backend: " CLI "}
	if err := o.Validate(context.Background(), &runtime.StaticRuntime{}, nil); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if o.Backend != agent.BackendCLI {
		t.Errorf("Backend = %q, want it normalised to %q", o.Backend, agent.BackendCLI)
	}
}

// TestBindingCommandsAreAdminOnly pins the gate at the command layer. The
// configuration refuses to start with the CLI backend enabled under permissive
// auth, but the annotation is the independent second lock: these two commands are
// how a chat conversation reaches a process on this host.
func TestBindingCommandsAreAdminOnly(t *testing.T) {
	out := runtime.NewStructuredResponder(&runtime.DynamicRuntime{})
	for _, cmd := range []*cobra.Command{NewNewCmd(&runtime.StaticRuntime{}, out), NewSwitchCmd(&runtime.StaticRuntime{}, out)} {
		if cmd.Annotations[chat.AdminOnly] != chat.True {
			t.Errorf("%s is not annotated %s", cmd.Name(), chat.AdminOnly)
		}
		if cmd.Annotations[chat.ChatGroup] != chat.ChatGroupAI {
			t.Errorf("%s is not grouped with the AI commands", cmd.Name())
		}
	}
}

// TestConversationKeyIsTheTurnsKey pins the key a binding is written under. It
// has to be the key an AI turn reads — the receiver's id — or the command would
// write a binding nothing consults and the next turn would keep running on the
// old backend.
func TestConversationKeyIsTheTurnsKey(t *testing.T) {
	// A Lark message: the receiver names the group chat, and ConvID mirrors it.
	dr := &runtime.DynamicRuntime{
		ConvID:   "oc_1",
		Sender:   v1.NewSender("ou_1", ""),
		Receiver: &v1.Receiver{Id: "oc_1"},
	}
	if got := conversationKey(dr); got != "oc_1" {
		t.Errorf("conversationKey() = %q, want the receiver id the turn uses", got)
	}

	// A REST command: no receiver, only a conversation id.
	if got := conversationKey(&runtime.DynamicRuntime{ConvID: "oc_2"}); got != "oc_2" {
		t.Errorf("conversationKey() = %q, want the conversation id", got)
	}
	if got := conversationKey(nil); got != "" {
		t.Errorf("conversationKey(nil) = %q, want empty", got)
	}
}

// runtimeCommandResult is a Responder that keeps what the command reported. The
// structured responder is the production equivalent, but it decorates the text
// on the way out; a test asserting on the message wants it verbatim.
type runtimeCommandResult struct {
	mu   sync.Mutex
	last runtime.CommandResult
}

func (r *runtimeCommandResult) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last.PlainText += string(p)
	return len(p), nil
}

func (r *runtimeCommandResult) SendText(_ context.Context, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last.PlainText += content
	return nil
}

func (r *runtimeCommandResult) SendCard(context.Context, string, map[string]any) error { return nil }

func (r *runtimeCommandResult) SendMarkdownCard(context.Context, string, string, string) error {
	return nil
}

func (r *runtimeCommandResult) SendInteractiveCard(context.Context, any) error { return nil }

func (r *runtimeCommandResult) SendResult(_ context.Context, res *runtime.CommandResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = *res
	return nil
}

func (r *runtimeCommandResult) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last.PlainText
}
