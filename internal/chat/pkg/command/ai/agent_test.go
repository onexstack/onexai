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
	"errors"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/clientset"
	"github.com/onexstack/onexai/internal/chat/pkg/roleset"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/store"
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	agentcli "github.com/onexstack/onexai/pkg/clientset/agent"
)

// The fakes below each embed the interface they stand in for, so only the
// methods a test actually drives are written out. A call to one of the others
// reaches a nil embedded interface and panics loudly, which is what a test
// wants: the command under test is supposed to touch exactly these.

// stubLark satisfies runtime.LarkServices without implementing any of it. The
// /agent command replies through the Responder, never through Lark, so
// HasLarkContext is the only thing that consults this value.
type stubLark struct{ runtime.LarkServices }

// stubAgentClient stands in for oai-agent-apiserver: which agents exist, and
// which of them the caller may use.
type stubAgentClient struct {
	agentcli.Interface

	// missing names agents GetAgent should report as absent.
	missing map[string]bool
	// denied maps an agent name to the reason CheckAgentAccess gives for it.
	denied map[string]string

	gets   []string
	checks []string
}

func (s *stubAgentClient) GetAgent(_ context.Context, name string) (*agentv1.Agent, error) {
	s.gets = append(s.gets, name)
	if s.missing[name] {
		return nil, agentcli.ErrBusy // any error stands for "no such agent"
	}
	return &agentv1.Agent{}, nil
}

func (s *stubAgentClient) CheckAgentAccess(_ context.Context, subjectID, agentName string) (bool, string, error) {
	s.checks = append(s.checks, subjectID+"/"+agentName)
	if reason, ok := s.denied[agentName]; ok {
		return false, reason, nil
	}
	return true, "", nil
}

// stubClientset wires that one client into the aggregate.
type stubClientset struct {
	clientset.Interface
	agent agentcli.Interface
}

// stubPeerStore stands in for the account-binding table: one row, or an error.
type stubPeerStore struct {
	store.PeerAgentStore
	row *model.PeerAgentM
	err error
}

func (s *stubPeerStore) GetByPeer(context.Context, string, string, string) (*model.PeerAgentM, error) {
	return s.row, s.err
}

func (s *stubClientset) Agent() agentcli.Interface { return s.agent }

// recordingSessions records the declaration writes. It is how "refs is not
// written" is asserted: not by reading a row back, but by the write never
// happening.
type recordingSessions struct {
	store.SessionStore
	writes []string
}

func (s *recordingSessions) SetRefs(_ context.Context, sessionID, refs string) error {
	s.writes = append(s.writes, sessionID+"="+refs)
	return nil
}

// recordingStore wires those stores into the aggregate.
type recordingStore struct {
	store.IStore
	session store.SessionStore
	// peers is nil unless a test needs a binding. The command asks for the
	// caller's account binding before it asks about permissions, and a nil store
	// is the "not bound" answer — which is why it is optional rather than
	// required.
	peers store.PeerAgentStore
}

func (s *recordingStore) Session() store.SessionStore      { return s.session }
func (s *recordingStore) PeerAgents() store.PeerAgentStore { return s.peers }

// agentFixture is one deployment: an admin in a Lark group whose session row
// already exists, with an agent service behind it.
type agentFixture struct {
	sr    *runtime.StaticRuntime
	dr    *runtime.DynamicRuntime
	cli   *stubAgentClient
	ses   *recordingSessions
	reply *runtimeCommandResult

	// note is what the deployment's scheduler says the declaration resolves to,
	// and noteCalls counts how often it was asked. The bot supplies this
	// callback in production; here it stands in for it.
	note      runtime.AgentResolution
	noteCalls int
}

func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()

	cli := &stubAgentClient{missing: map[string]bool{}, denied: map[string]string{}}
	ses := &recordingSessions{}
	f := &agentFixture{
		cli:   cli,
		ses:   ses,
		reply: &runtimeCommandResult{},
		sr: &runtime.StaticRuntime{
			Roles:     roleset.NewStatic(nil, map[string]struct{}{"ou_admin": {}}),
			Store:     &recordingStore{session: ses},
			Clientset: &stubClientset{agent: cli},
		},
		dr: &runtime.DynamicRuntime{
			Sender:       v1.NewSender("ou_admin", ""),
			Receiver:     &v1.Receiver{Id: "oc_group_1"},
			LarkServices: &stubLark{},
			SessionM:     &model.SessionM{SessionID: "oc_group_1"},
		},
	}
	f.dr.ResolvedAgents = func(context.Context, *runtime.DynamicRuntime) (runtime.AgentResolution, error) {
		f.noteCalls++
		return f.note, nil
	}
	return f
}

// run executes /agent the way the command does, through the options.
func (f *agentFixture) run(t *testing.T, override, reset bool, args ...string) error {
	t.Helper()
	o := &AgentOptions{out: f.reply, Override: override, Reset: reset}
	if err := o.Complete(context.Background(), args); err != nil {
		return err
	}
	if err := o.Validate(context.Background(), args); err != nil {
		return err
	}
	return o.Run(context.Background(), f.sr, f.dr, args)
}

// written returns the declaration the command recorded, or "" when it recorded
// none.
func (f *agentFixture) written() string {
	if len(f.ses.writes) == 0 {
		return ""
	}
	return f.ses.writes[len(f.ses.writes)-1]
}

// markdown is the card body the command reported. It is separate from the plain
// text because the two say different things to a reader: the plain text is the
// headline, the card body is the list of what was applied and what was not.
func (r *runtimeCommandResult) markdown() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last.Markdown == nil {
		return ""
	}
	return r.last.Markdown.Content
}

// TestAgentCommandRecordsADeclaration is the happy path, and pins that the
// stored form is the reference's canonical rendering rather than the raw
// argument.
func TestAgentCommandRecordsADeclaration(t *testing.T) {
	f := newAgentFixture(t)

	if err := f.run(t, false, false, "ops-agent", "@peer"); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got, want := f.written(), `oc_group_1=["ops-agent","@peer"]`; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
	if got := f.dr.SessionM.Refs; got != `["ops-agent","@peer"]` {
		t.Errorf("the in-memory snapshot = %q, want it kept in step with the write", got)
	}
	if !strings.Contains(f.reply.text(), "ops-agent") {
		t.Errorf("reply = %q, want it to name what was applied", f.reply.text())
	}
	// The literal was checked; the reference was not, because a reference is an
	// intent about who will be speaking and there is no name to check yet.
	if len(f.cli.gets) != 1 || f.cli.gets[0] != "ops-agent" {
		t.Errorf("GetAgent calls = %v, want just the literal", f.cli.gets)
	}
	if len(f.cli.checks) != 1 || f.cli.checks[0] != "ou_admin/ops-agent" {
		t.Errorf("access checks = %v, want the literal checked as the caller", f.cli.checks)
	}
}

// TestAgentAsksAboutTheBoundAccount pins which identity the permission question
// is asked about.
//
// A speaker whose Lark account is bound to a platform account is authorized as
// that account: it is what the rules name and what the scheduler hands the agent
// service for the run itself. Asking about the raw sender instead made /agent
// refuse agents the run would have allowed, which reads to the speaker as "no
// permission" about an agent they were already using.
func TestAgentAsksAboutTheBoundAccount(t *testing.T) {
	f := newAgentFixture(t)
	// The same speaker, now carrying the handle their binding was written under.
	f.dr.Sender = v1.NewSender(f.dr.Identity(), "ou_bound")
	f.sr.Store = &recordingStore{
		session: f.ses,
		peers:   &stubPeerStore{row: &model.PeerAgentM{Canonical: "user-mkc1kj"}},
	}

	if err := f.run(t, false, false, "onex-x-max-tier"); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if len(f.cli.checks) != 1 || f.cli.checks[0] != "user-mkc1kj/onex-x-max-tier" {
		t.Errorf("access checks = %v, want the bound account rather than the sender", f.cli.checks)
	}
	if got, want := f.written(), `oc_group_1=["onex-x-max-tier"]`; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

// TestAgentFallsBackToTheSenderWithoutABinding pins the other half of the rule:
// with no binding the question is about the tenant identity, which is what the
// rules for internal callers are keyed by.
func TestAgentFallsBackToTheSenderWithoutABinding(t *testing.T) {
	f := newAgentFixture(t)
	f.dr.Sender = v1.NewSender(f.dr.Identity(), "ou_unbound")
	f.sr.Store = &recordingStore{session: f.ses, peers: &stubPeerStore{}}

	if err := f.run(t, false, false, "ops-agent"); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if len(f.cli.checks) != 1 || f.cli.checks[0] != "ou_admin/ops-agent" {
		t.Errorf("access checks = %v, want the tenant identity", f.cli.checks)
	}
}

// TestAgentRefusesWhenTheBindingCannotBeRead pins that a lookup which failed is
// not answered as "not bound". Answering with the tenant identity would deny an
// agent the run allows, and report it as a permission the speaker does not have
// — a confident wrong answer about somebody else's authority.
func TestAgentRefusesWhenTheBindingCannotBeRead(t *testing.T) {
	f := newAgentFixture(t)
	// The same speaker, now carrying the handle their binding was written under.
	f.dr.Sender = v1.NewSender(f.dr.Identity(), "ou_bound")
	f.sr.Store = &recordingStore{
		session: f.ses,
		peers:   &stubPeerStore{err: errors.New("store is down")},
	}

	err := f.run(t, false, false, "ops-agent")
	if err == nil {
		t.Fatal("a failed binding lookup was answered as a declaration")
	}
	if len(f.cli.checks) != 0 {
		t.Errorf("access checks = %v, want none when the subject is unknown", f.cli.checks)
	}
}

// TestAgentCommandIsAdminOnly is the gate that makes the declaration mean
// something. Anyone in a group can type a command, so without it any member
// could point the conversation at an agent of their choosing.
func TestAgentCommandIsAdminOnly(t *testing.T) {
	f := newAgentFixture(t)
	// A member of the group who is nobody's admin.
	f.dr.Sender = v1.NewSender("ou_member", "")

	err := f.run(t, false, false, "ops-agent")
	if err == nil {
		t.Fatal("a non-admin was allowed to declare an agent")
	}
	if !runtime.IsUserError(err) {
		t.Errorf("error = %v, want a user-facing refusal", err)
	}
	if !strings.Contains(err.Error(), "管理员") {
		t.Errorf("error = %q, want it to say who may run this", err)
	}
	if got := f.written(); got != "" {
		t.Errorf("wrote %q, want the declaration untouched", got)
	}
}

// TestAgentCommandNeedsALarkGroup pins the two contexts it cannot work without.
// A declaration lives on a session row, and the row is keyed by the chat the
// message arrived in — outside Lark there is neither.
func TestAgentCommandNeedsALarkGroup(t *testing.T) {
	t.Run("no Lark context", func(t *testing.T) {
		f := newAgentFixture(t)
		f.dr.LarkServices = nil

		err := f.run(t, false, false, "ops-agent")
		if !runtime.IsUserError(err) || !strings.Contains(err.Error(), "Lark") {
			t.Fatalf("error = %v, want a refusal naming the context it needs", err)
		}
		if got := f.written(); got != "" {
			t.Errorf("wrote %q, want nothing", got)
		}
	})

	t.Run("no session row yet", func(t *testing.T) {
		f := newAgentFixture(t)
		f.dr.SessionM = nil

		err := f.run(t, false, false, "ops-agent")
		if !runtime.IsUserError(err) {
			t.Fatalf("error = %v, want a user-facing refusal", err)
		}
		if !strings.Contains(err.Error(), "发一条消息") {
			t.Errorf("error = %q, want it to say how to make the row exist", err)
		}
		if got := f.written(); got != "" {
			t.Errorf("wrote %q, want nothing", got)
		}
	})
}

// TestAgentCommandRefusesAnUnknownReferenceKind pins the check that turns a typo
// into an immediate refusal. Without it the misspelling would be stored and only
// surface on the next message, as a turn that failed for a reason nobody
// remembers typing.
func TestAgentCommandRefusesAnUnknownReferenceKind(t *testing.T) {
	f := newAgentFixture(t)

	err := f.run(t, false, false, "@pear")
	if err == nil {
		t.Fatal("a misspelled reference kind was accepted")
	}
	if !strings.Contains(err.Error(), "pear") {
		t.Errorf("error = %q, want it to quote the offending entry", err)
	}
	for _, kind := range []string{"peer", "oncall", "default"} {
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("error = %q, want it to list %q among the usable kinds", err, kind)
		}
	}
	if got := f.written(); got != "" {
		t.Errorf("wrote %q, want nothing", got)
	}
	// A refused kind is refused before any name is looked up: there is no name.
	if len(f.cli.gets) != 0 {
		t.Errorf("GetAgent calls = %v, want none for a reference", f.cli.gets)
	}
}

// TestAgentCommandSkipsAnAgentThatDoesNotExist and the access case below are the
// two halves of "loaded by name": the agent has to be there, and the caller has
// to be allowed it.
func TestAgentCommandSkipsAnAgentThatDoesNotExist(t *testing.T) {
	f := newAgentFixture(t)
	f.cli.missing["ghost-agent"] = true

	err := f.run(t, false, false, "ghost-agent")
	if err == nil {
		t.Fatal("an agent that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "ghost-agent") || !strings.Contains(err.Error(), "未改动") {
		t.Errorf("error = %q, want it to name the entry and say the declaration was not changed", err)
	}
	if got := f.written(); got != "" {
		t.Errorf("wrote %q, want nothing", got)
	}
}

// TestAgentCommandSkipsAnAgentTheCallerMayNotUse pins the third of the three
// checks, and that the reason the agent service gave reaches the person who
// typed it rather than being flattened into "denied".
func TestAgentCommandSkipsAnAgentTheCallerMayNotUse(t *testing.T) {
	f := newAgentFixture(t)
	f.cli.denied["onex-u-user-someone-else"] = "该 Agent 仅限其订阅者"

	err := f.run(t, false, false, "onex-u-user-someone-else")
	if err == nil {
		t.Fatal("an agent the caller may not use was accepted")
	}
	if !strings.Contains(err.Error(), "仅限其订阅者") {
		t.Errorf("error = %q, want the agent service's own reason", err)
	}
	if got := f.written(); got != "" {
		t.Errorf("wrote %q, want nothing", got)
	}
}

// TestAgentCommandWritesTheValidSubsetAndReportsTheRest pins what a partly-bad
// run does. This is a decision, not an accident: the command was specified as
// refusing the whole run, and writing the valid subset was chosen instead
// (2026-10-07).
//
// A single misspelling must not throw away the entries that were fine, and the
// card names what was skipped so the person can fix it. What must not happen is
// the silent version — writing the subset without saying so — which is the half
// of the refused-the-run design worth keeping.
func TestAgentCommandWritesTheValidSubsetAndReportsTheRest(t *testing.T) {
	f := newAgentFixture(t)
	f.cli.missing["ghost-agent"] = true

	if err := f.run(t, false, false, "ops-agent", "ghost-agent", "@pear"); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got, want := f.written(), `oc_group_1=["ops-agent"]`; got != want {
		t.Errorf("wrote %q, want only the entry that passed", got)
	}
	body := f.reply.markdown()
	for _, skipped := range []string{"ghost-agent", "@pear"} {
		if !strings.Contains(body, skipped) {
			t.Errorf("card body = %q, want it to name the skipped entry %q", body, skipped)
		}
	}
}

// TestAgentCommandMergesByDefault pins that a second run adds to the first
// rather than replacing it, and that adding something already declared does not
// duplicate it. Position is meaning — the first entry is the one that answers
// when several resolve — so a re-add has to leave the original position alone.
func TestAgentCommandMergesByDefault(t *testing.T) {
	f := newAgentFixture(t)
	f.dr.SessionM.Refs = model.EncodeNameList([]string{"ops-agent", "@peer"})

	if err := f.run(t, false, false, "@peer", "bke-agent"); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got, want := f.written(), `oc_group_1=["ops-agent","@peer","bke-agent"]`; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

// TestAgentCommandOverrideReplaces pins --override, which is how a declaration
// that has accumulated entries nobody remembers is reset to one intention.
func TestAgentCommandOverrideReplaces(t *testing.T) {
	f := newAgentFixture(t)
	f.dr.SessionM.Refs = model.EncodeNameList([]string{"ops-agent", "@peer"})

	if err := f.run(t, true, false, "bke-agent"); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got, want := f.written(), `oc_group_1=["bke-agent"]`; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

// TestAgentCommandResetGoesBackToTheTemplate pins --reset, and that it writes an
// empty declaration rather than the template's own list: an empty column keeps
// following the conversation type, a written-out one freezes today's answer.
func TestAgentCommandResetGoesBackToTheTemplate(t *testing.T) {
	f := newAgentFixture(t)
	f.dr.SessionM.Refs = model.EncodeNameList([]string{"ops-agent"})

	if err := f.run(t, false, true); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got, want := f.written(), "oc_group_1="; got != want {
		t.Errorf("wrote %q, want an empty declaration", got)
	}
	if got := f.dr.SessionM.Refs; got != "" {
		t.Errorf("the in-memory snapshot = %q, want it cleared too", got)
	}
}

// TestAgentCommandReportsWhatTheDeclarationResolvesTo pins the half of the
// command's effect that is invisible from inside it.
//
// Writing `@peer` records an intent. Whether that intent picks an agent depends
// on the speaker and on the audience, which is the scheduler's business — so
// the command asks the deployment and renders the answer, and the person who
// typed it learns what they actually asked for while they are still reading.
func TestAgentCommandReportsWhatTheDeclarationResolvesTo(t *testing.T) {
	note := runtime.AgentResolution{Agents: []string{"onex-u-user-a1b2c3"}}

	t.Run("a declaration that picks an agent", func(t *testing.T) {
		f := newAgentFixture(t)
		f.note = note

		if err := f.run(t, false, false, "ops-agent"); err != nil {
			t.Fatalf("Run() = %v", err)
		}
		if f.noteCalls != 1 {
			t.Errorf("the deployment was asked %d times, want once", f.noteCalls)
		}
		if body, line := f.reply.markdown(), note.Line(); !strings.Contains(body, line) {
			t.Errorf("card body = %q, want it to carry %q", body, line)
		}
	})

	t.Run("a reset reports the template's answer", func(t *testing.T) {
		// --reset changes the declaration too, so the same question applies: the
		// template it falls back to may pick an agent as well.
		f := newAgentFixture(t)
		f.note = note

		if err := f.run(t, false, true); err != nil {
			t.Fatalf("Run() = %v", err)
		}
		if body, line := f.reply.markdown(), note.Line(); !strings.Contains(body, line) {
			t.Errorf("card body = %q, want it to carry %q", body, line)
		}
	})

	t.Run("nothing was written, so nothing is asked", func(t *testing.T) {
		f := newAgentFixture(t)
		f.cli.missing["ghost-agent"] = true

		if err := f.run(t, false, false, "ghost-agent"); err == nil {
			t.Fatal("Run() accepted an agent that does not exist")
		}
		if f.noteCalls != 0 {
			t.Errorf("the deployment was asked %d times for a run that wrote nothing", f.noteCalls)
		}
	})

	t.Run("a deployment with no scheduler still works", func(t *testing.T) {
		// Every surface that is not a Lark message has no scheduler behind it,
		// and /agent answers those with a refusal before reaching here — but the
		// reply must not depend on the callback being set.
		f := newAgentFixture(t)
		f.dr.ResolvedAgents = nil

		if err := f.run(t, false, false, "ops-agent"); err != nil {
			t.Fatalf("Run() = %v", err)
		}
		if body := f.reply.markdown(); strings.Contains(body, "解析为") {
			t.Errorf("card body = %q, want no resolution line", body)
		}
	})
}

// TestAgentCommandValidate pins the two runs that are rejected before anything
// is looked up: a reset that also names agents, and a run that names nothing.
func TestAgentCommandValidate(t *testing.T) {
	tests := []struct {
		name      string
		reset     bool
		args      []string
		wantInErr string
	}{
		{"reset with arguments", true, []string{"ops-agent"}, "--reset"},
		{"neither a reference nor a reset", false, nil, "至少一个"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &AgentOptions{out: &runtimeCommandResult{}, Reset: tt.reset}
			err := o.Validate(context.Background(), tt.args)
			if err == nil {
				t.Fatal("Validate() accepted the run")
			}
			if !runtime.IsUserError(err) || !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("error = %v, want a user-facing refusal mentioning %q", err, tt.wantInErr)
			}
		})
	}
}

// TestAgentCommandSaysWhenAPrivateChatBindingOutranksTheDeclaration covers the
// reply's new sentence.
//
// A declaration is still stored — it is the fallback for an unbound speaker —
// but in a private chat a usable binding is what actually answers. Saying so in
// the same reply is the difference between "the command worked" and "the
// command changed something that has no effect right now".
func TestAgentCommandSaysWhenAPrivateChatBindingOutranksTheDeclaration(t *testing.T) {
	const hint = "私聊里，你飞书账号的绑定优先于本会话的声明"

	t.Run("binding preferred", func(t *testing.T) {
		f := newAgentFixture(t)
		f.note = runtime.AgentResolution{
			Refs:             []string{"@peer"},
			Agents:           []string{"fz-admin"},
			BindingPreferred: true,
		}

		if err := f.run(t, false, false, "fz-public"); err != nil {
			t.Fatalf("Run() = %v", err)
		}
		if body := f.reply.markdown(); !strings.Contains(body, hint) {
			t.Errorf("card body = %q, want it to explain that the binding wins", body)
		}
	})

	t.Run("declaration in effect", func(t *testing.T) {
		f := newAgentFixture(t)
		f.note = runtime.AgentResolution{Agents: []string{"ops-agent"}}

		if err := f.run(t, false, false, "ops-agent"); err != nil {
			t.Fatalf("Run() = %v", err)
		}
		if body := f.reply.markdown(); strings.Contains(body, hint) {
			t.Errorf("card body = %q, want no binding-precedence note", body)
		}
	})
}
