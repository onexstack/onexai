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

package bot

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// funnelFixture is one group conversation wired to a real session manager, so
// the ladder runs against the real lock, queue and permission bookkeeping rather
// than against stubs of them.
//
// There is no Lark fake here because the ladder sends nothing: a rung that has
// something to say to the user returns it as the handoff the caller performs.
type funnelFixture struct {
	biz  *botBiz
	llm  *fakeAgent
	sess *session.Session
	conv string
}

const funnelConvID = "oc_funnel_conv"

func newFunnelFixture(t *testing.T, tune ...func(*runtime.StaticRuntime)) *funnelFixture {
	t.Helper()

	llm := &fakeAgent{}
	sr := &runtime.StaticRuntime{
		Store:     &fakeStore{session: &fakeSessionStore{}},
		Clientset: &fakeClientset{llm: llm},
		Agents:    llmRegistry(llm),
		BotName:   "Helper",
	}
	for _, fn := range tune {
		fn(sr)
	}

	mgr := session.NewManager(context.Background(), session.Options{
		DataDir:           t.TempDir(),
		MaxQueuedMessages: 5,
	})
	t.Cleanup(mgr.Close)

	sess, err := mgr.Session(funnelConvID)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sr.Sessions = mgr

	return &funnelFixture{
		sess: sess,
		llm:  llm,
		conv: funnelConvID,
		biz: &botBiz{
			sr:       sr,
			recalled: make(map[string]time.Time),
		},
	}
}

// walk runs one message down the ladder and reports the rung that decided it,
// along with the handoff that rung would carry out.
//
// The handoff is returned rather than run: this file is about which rung claims a
// message, and running it would drag in the platform connection the ladder
// deliberately does not need. What the handoffs do once started is covered by
// ai_queue_test.go, which has a transport to do it against.
func (f *funnelFixture) walk(t *testing.T, ev *im.Event) (funnelStep, string, func() error) {
	t.Helper()
	step, outcome, act, err := f.biz.walk(context.Background(), newInbound(ev, time.Now()))
	if err != nil {
		// Every rung decides without touching the platform, so a rung that fails
		// here has failed at something else.
		t.Fatalf("the rung %q failed to decide the message's fate: %v", step, err)
	}
	return step, outcome, act
}

// messageEvent builds the event the handler would deliver for a group text
// message. Group rather than P2P because a group conversation is keyed by its
// chat id: the session the ladder looks up is then the one this fixture created.
func messageEvent(text string) *im.Event {
	return &im.Event{
		Type:   im.EventTypeMessage,
		ConvID: funnelConvID,
		Message: &im.Message{
			ID:       "om_" + text,
			ConvID:   funnelConvID,
			ChatType: im.ConvTypeGroup,
			// SenderID is the identity (user_id); SenderOpenID is only the
			// platform handle, and a test that confused the two would pass while
			// production sent an ou_… where Lark wanted a user_id.
			SenderID:     "u_sender",
			SenderOpenID: "ou_sender",
			Content:      im.MessageContent{Type: "text", Text: text, Raw: `{"text":"` + text + `"}`},
		},
	}
}

// TestInboundIdentityIsUserID pins which of the sender's two Lark ids the bot
// acts on. The pair is carried on the event and both are plausible-looking
// strings, so nothing but a test stops the wrong one being picked — and picking
// the open_id is not cosmetic: creating the ticket group /oncall opens is a
// call that takes user_ids, and it refused every ticket ever raised with
// "not a valid {user_id}" while the identity was an ou_… value.
func TestInboundIdentityIsUserID(t *testing.T) {
	f := newFunnelFixture(t)
	ctx := context.Background()

	group, err := f.biz.buildInboundRuntime(ctx, messageEvent("你好"), nil)
	if err != nil {
		t.Fatalf("buildInboundRuntime(group) returned error: %v", err)
	}
	if got := group.Sender.Identity(); got != "u_sender" {
		t.Errorf("Identity() = %q, want %q (the user_id, not the open_id)", got, "u_sender")
	}
	if got := group.Address(); got != funnelConvID {
		t.Errorf("Id = %q, want %q (a group is addressed by its chat)", got, funnelConvID)
	}

	// The same identity addresses a direct message, and it is sent as a
	// user_id — matching the id, or Lark reads an ou_… value in the user_id
	// namespace and refuses the call.
	p2p := messageEvent("你好")
	p2p.Message.ChatType = im.ConvTypeP2P
	direct, err := f.biz.buildInboundRuntime(ctx, p2p, nil)
	if err != nil {
		t.Fatalf("buildInboundRuntime(p2p) returned error: %v", err)
	}
	if got := direct.Sender.Identity(); got != "u_sender" {
		t.Errorf("Identity() = %q, want %q", got, "u_sender")
	}
	if got := direct.Address(); got != "u_sender" {
		t.Errorf("Id = %q, want %q", got, "u_sender")
	}
	if got := direct.Receiver.Type; got != v1.ReceiverTypeUserID {
		t.Errorf("Type = %q, want %q (Id is sent as the receive_id of this type)", got, v1.ReceiverTypeUserID)
	}
}

// TestInboundDirectAddressFallsBackToOpenID pins where a reply to a sender with
// no user_id is addressed. A person outside this tenant is reported with an
// open_id and an empty user_id — Lark scopes a user_id to its tenant — and
// addressing their reply by the empty user_id is a reply that goes nowhere, which
// is what the bot reported as "receiver has no id" while the log said only that
// the identity was unresolved.
//
// Only the address falls back. The identity stays empty, because it is the id
// every authorization, record and allow-list names: filling it with an open_id is
// how one person acquires two identities.
func TestInboundDirectAddressFallsBackToOpenID(t *testing.T) {
	f := newFunnelFixture(t)

	p2p := messageEvent("你好")
	p2p.Message.ChatType = im.ConvTypeP2P
	p2p.Message.SenderID = ""

	direct, err := f.biz.buildInboundRuntime(context.Background(), p2p, nil)
	if err != nil {
		t.Fatalf("buildInboundRuntime(p2p) returned error: %v", err)
	}
	if got := direct.Address(); got != "ou_sender" {
		t.Errorf("Id = %q, want %q (the only address this sender has)", got, "ou_sender")
	}
	if got := direct.Receiver.Type; got != v1.ReceiverTypeOpenID {
		t.Errorf("Type = %q, want %q (an ou_… value sent in the user_id namespace is refused)",
			got, v1.ReceiverTypeOpenID)
	}
	if got := direct.Sender.Identity(); got != "" {
		t.Errorf("Identity() = %q, want empty: the address falls back, the identity does not", got)
	}
}

// TestAllowlistReportsAnUnresolvedSenderDistinctly pins that a sender with no
// user_id is dropped under its own reason. An allow-list names people by user_id,
// which is the one id somebody outside this tenant does not have — so they can
// never match it, and a drop logged as a plain "sender" sends whoever maintains
// the list hunting for a name that is not what went wrong.
func TestAllowlistReportsAnUnresolvedSenderDistinctly(t *testing.T) {
	f := newFunnelFixture(t, func(sr *runtime.StaticRuntime) {
		sr.AllowFrom = map[string]struct{}{"u_someone": {}}
	})
	logs := captureLogs(t)

	ev := messageEvent("你好")
	ev.Message.SenderID = ""

	step, outcome, act := f.walk(t, ev)
	if step != stepAllowlist || outcome != outcomeDropped {
		t.Errorf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepAllowlist, outcomeDropped)
	}
	if act != nil {
		t.Error("a message dropped by the allow-list was handed off anyway")
	}
	if got := logs.String(); !strings.Contains(got, "sender_unresolved") {
		t.Errorf("the drop was not reported as an unresolved identity: %s", got)
	}
}

// captureLogs routes the default logger into a buffer for the duration of a test.
// The ladder reports its drops through the default logger rather than a return
// value, so this is the only way to pin how a drop is described.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// mentioningBot marks the event as addressed to the bot, which is what a group
// conversation has to do to reach the prompt rung.
func mentioningBot(ev *im.Event) *im.Event {
	ev.Message.Mentions = []im.Mention{{Key: "@_user_1", Name: "Helper", IsBot: true}}
	return ev
}

// TestFunnelGuardDropsAnEventWithNoConversation pins the rung that replaced a
// panic: a message with no chat cannot be answered, and the code that used to
// handle it dereferenced the missing chat id.
func TestFunnelGuardDropsAnEventWithNoConversation(t *testing.T) {
	f := newFunnelFixture(t)

	ev := messageEvent("你好")
	ev.ConvID = ""

	step, outcome, act := f.walk(t, ev)
	if step != stepGuard || outcome != outcomeDropped {
		t.Errorf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepGuard, outcomeDropped)
	}
	if act != nil {
		t.Error("a message with no conversation was handed off anyway")
	}

	if err := f.biz.ProcessMessage(context.Background(), nil); err != nil {
		t.Errorf("ProcessMessage(nil) returned error: %v", err)
	}
}

// TestRecallShortCircuits is the recall rung: a message withdrawn by its sender
// must not be answered, even when a duplicate delivery arrives behind the recall.
func TestRecallShortCircuits(t *testing.T) {
	f := newFunnelFixture(t)

	ev := mentioningBot(messageEvent("撤回我"))
	f.biz.markMessageRecalled(ev.Message.ID)

	step, outcome, act := f.walk(t, ev)
	if step != stepRecall || outcome != outcomeDropped {
		t.Errorf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepRecall, outcomeDropped)
	}
	if act != nil {
		t.Error("a recalled message was handed off anyway")
	}
}

// TestStaleMessageDropped pins the staleness bound: a re-delivered backlog is not
// a queue of questions anyone is still waiting on.
func TestStaleMessageDropped(t *testing.T) {
	f := newFunnelFixture(t, func(sr *runtime.StaticRuntime) { sr.MaxEventAge = time.Minute })

	stale := mentioningBot(messageEvent("很久以前的问题"))
	stale.Message.CreateTime = time.Now().Add(-2 * time.Hour)
	step, outcome, act := f.walk(t, stale)
	if step != stepStale || outcome != outcomeDropped {
		t.Errorf("stale message: walk() = (%s, %s), want (%s, %s)", step, outcome, stepStale, outcomeDropped)
	}
	if act != nil {
		t.Error("a stale message was handed off anyway")
	}

	fresh := mentioningBot(messageEvent("刚刚的问题"))
	fresh.Message.CreateTime = time.Now()
	if step, _, act := f.walk(t, fresh); step != stepPrompt || act == nil {
		t.Errorf("a fresh message stopped at %q, handed off %v; want %q and a handoff", step, act != nil, stepPrompt)
	}
}

// TestStaleBoundDisabled answers everything when no bound is configured.
func TestStaleBoundDisabled(t *testing.T) {
	f := newFunnelFixture(t)

	old := mentioningBot(messageEvent("很久以前的问题"))
	old.Message.CreateTime = time.Now().Add(-72 * time.Hour)

	if step, _, act := f.walk(t, old); step != stepPrompt || act == nil {
		t.Errorf("walk() with no staleness bound stopped at %q, handed off %v; want %q and a handoff",
			step, act != nil, stepPrompt)
	}
}

// TestAllowlistBlocksSender and its siblings pin the three access rules to one
// rung. They are silent by design, which is why the rung exists as a countable
// step rather than as three scattered returns.
func TestAllowlistBlocksSender(t *testing.T) {
	f := newFunnelFixture(t, func(sr *runtime.StaticRuntime) {
		sr.AllowFrom = map[string]struct{}{"ou_somebody_else": {}}
	})

	ev := mentioningBot(messageEvent("你好"))
	step, outcome, act := f.walk(t, ev)
	if step != stepAllowlist || outcome != outcomeDropped {
		t.Errorf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepAllowlist, outcomeDropped)
	}
	if act != nil {
		t.Error("a message from a disallowed sender was handed off anyway")
	}
}

func TestAllowlistBlocksChat(t *testing.T) {
	f := newFunnelFixture(t, func(sr *runtime.StaticRuntime) {
		sr.AllowChat = map[string]struct{}{"oc_another_group": {}}
	})

	ev := mentioningBot(messageEvent("你好"))
	if step, outcome, _ := f.walk(t, ev); step != stepAllowlist || outcome != outcomeDropped {
		t.Errorf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepAllowlist, outcomeDropped)
	}
}

func TestGroupOnlyDropsP2P(t *testing.T) {
	f := newFunnelFixture(t, func(sr *runtime.StaticRuntime) { sr.GroupOnly = true })

	p2p := messageEvent("你好")
	p2p.Message.ChatType = im.ConvTypeP2P
	if step, outcome, _ := f.walk(t, p2p); step != stepAllowlist || outcome != outcomeDropped {
		t.Errorf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepAllowlist, outcomeDropped)
	}
}

// TestAllowlistDoesNotBlockCommands pins the boundary that a group which never
// mentions the bot can still use /commands: the allowlist is about who may talk
// to the bot, the mention is about who it answers.
func TestAllowlistDoesNotBlockCommands(t *testing.T) {
	f := newFunnelFixture(t)

	// No @-mention: this is a group message addressed to the command tree.
	step, outcome, act := f.walk(t, messageEvent("/help"))
	if step != stepCommand || outcome != outcomeHandled {
		t.Errorf("an unmentioned /command stopped at (%s, %s), want (%s, %s)",
			step, outcome, stepCommand, outcomeHandled)
	}
	if act == nil {
		t.Error("the command was not handed to the command tree")
	}
}

// pendingApproval blocks the conversation on an interrupt, recorded the way a
// paused run records it: a run is parked first, and the question is recorded
// against it.
//
// The run is parked rather than merely named because that is what the answer
// addresses. The port refuses an answer for a conversation with no run of its
// own in flight — it has no sink to deliver the continuation to — so a fixture
// that recorded the question without the run behind it would be describing a
// state the service cannot be in.
func (f *funnelFixture) pendingApproval(t *testing.T, kind string) {
	t.Helper()
	const runID = "run-1"
	f.parkRun(t, runID)

	dr := &runtime.DynamicRuntime{
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: f.conv},
	}
	question := "允许执行 rm 吗？"
	req := agent.InterruptRequest{
		ToolName: "Bash",
		Question: question,
		Kind:     kind,
	}
	if kind == agent.InterruptKindQuestion {
		// A question is answered by naming the interrupt point it came from; an
		// approval carries no address, and the answer travels as the decision.
		req.Address = "ask:1"
		req.Options = []string{"1", "2", "3"}
	}
	f.biz.recordPendingPermission(dr, &session.PendingPermission{
		RunID:     runID,
		RequestID: "req-1",
		Kind:      kind,
		ToolName:  req.ToolName,
		Question:  question,
		Address:   req.Address,
		AskedBy:   "ou_asker",
	})

	if f.sess.PendingPermission() == nil {
		t.Fatal("the fixture could not record a pending interrupt")
	}
}

// parkRun starts a run and lets it finish, which is what leaves the backend
// holding the sink a later answer continues into. A paused run looks like this
// from this side: the stream that started it is over, and the conversation still
// has the run it can be answered on.
func (f *funnelFixture) parkRun(t *testing.T, runID string) {
	t.Helper()
	conv, _, err := f.biz.agentSession(context.Background(), f.conv)
	if err != nil {
		t.Fatalf("the fixture could not reach the agent backend: %v", err)
	}
	if err := conv.AskStream(context.Background(), agent.AskRequest{RunID: runID},
		func(agent.Event) error { return nil }); err != nil {
		t.Fatalf("the fixture could not park a run: %v", err)
	}
}

// TestPendingPermissionEatsPlainText is the fix for a gap that had no way to
// answer a paused run except by finding the right button: a plain "允许" (allow) is
// delivered to the run that is waiting for it.
func TestPendingPermissionEatsPlainText(t *testing.T) {
	f := newFunnelFixture(t)
	f.pendingApproval(t, "")

	step, outcome, act := f.walk(t, messageEvent("允许"))
	if step != stepPerm || outcome != outcomeHandled {
		t.Fatalf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepPerm, outcomeHandled)
	}
	got := f.llm.awaitApprovals(t, 1)[0]
	// Both fields, because they are separate arguments: the run names what is
	// being answered and the decision is what the backend acts on. Asserting only
	// one of them is how a "允许" (allow) test passes while the decision travelling beside
	// it says something else entirely — and the run id is what keeps the answer
	// from being applied to whatever the conversation runs next.
	if got.runID != "run-1" || got.action != agent.DecisionAllow {
		t.Errorf("delivered (run %q, decision %q), want run-1 allowed", got.runID, got.action)
	}
	if act != nil {
		t.Error("an answer to a pending approval was also handed off as a prompt")
	}
	if got := f.sess.PendingPermission(); got != nil {
		t.Errorf("the conversation is still waiting on %+v after being answered", got)
	}
}

// TestPendingPermissionCarriesTheDenyDecision is the security half of the test
// above, and the one that was missing: a refused approval must reach the backend
// as a refusal. An approval answered with the wrong decision is not a cosmetic
// bug — the tool the user just refused runs anyway.
func TestPendingPermissionCarriesTheDenyDecision(t *testing.T) {
	for _, answer := range []string{"拒绝", "deny", "不行", "no"} {
		t.Run(answer, func(t *testing.T) {
			f := newFunnelFixture(t)
			f.pendingApproval(t, "")

			if step, outcome, _ := f.walk(t, messageEvent(answer)); step != stepPerm || outcome != outcomeHandled {
				t.Fatalf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepPerm, outcomeHandled)
			}
			if got := f.llm.awaitApprovals(t, 1)[0]; got.action != agent.DecisionDeny {
				t.Errorf("the run was resumed with decision %q, want %q — a refused tool must not be approved",
					got.action, agent.DecisionDeny)
			}
		})
	}
}

// TestPendingPermissionCarriesTheAllowAllDecision covers the third intent. It is
// not a synonym for allow: allow-all is what stops the backend asking about that
// tool again, so delivering it as a plain allow would keep re-prompting, and
// delivering a plain allow as allow-all would silence prompts the user never
// agreed to stop seeing.
func TestPendingPermissionCarriesTheAllowAllDecision(t *testing.T) {
	f := newFunnelFixture(t)
	f.pendingApproval(t, "")

	if _, outcome, _ := f.walk(t, messageEvent("总是允许")); outcome != outcomeHandled {
		t.Fatalf("outcome = %s, want %s", outcome, outcomeHandled)
	}
	if got := f.llm.awaitApprovals(t, 1)[0]; got.action != agent.DecisionAllowAll {
		t.Errorf("decision = %q, want %q", got.action, agent.DecisionAllowAll)
	}
}

// TestPendingPermissionDoesNotEatSlashCommand is the load-bearing ordering test:
// with a run paused, a "/command" must still reach the command tree. Without the
// guard on the permission rung, /stop — the one command a user most wants while
// something is blocked — would be typed into the agent as the literal answer
// "stop", and the run would stay blocked.
func TestPendingPermissionDoesNotEatSlashCommand(t *testing.T) {
	f := newFunnelFixture(t)
	f.pendingApproval(t, "")

	ev := messageEvent("/help")
	step, _, _ := f.walk(t, ev)
	if step == stepPerm {
		t.Fatal("a /command was consumed as the answer to a pending approval")
	}
	if step != stepCommand {
		t.Fatalf("walk() stopped at %q, want %q", step, stepCommand)
	}
	if len(f.llm.approvals) != 0 {
		t.Errorf("a /command was delivered to the paused run: %+v", f.llm.approvals)
	}
	if f.sess.PendingPermission() == nil {
		t.Error("a /command cleared the pending approval it did not answer")
	}
}

// TestApprovalWordIsNotGuessed pins the other half of the decision: an approval
// is a security boundary, so text that is not recognisably a decision is neither
// allowed nor refused.
//
// What it is, since the takeover rework, is a new request. The rung declines the
// message rather than answering it, so the text is not delivered to the parked
// run as a decision; it falls through the ladder to the prompt rung and the run
// is left parked for runAI to cancel on its way past. Before the rework the rung
// claimed the message and sent a "没看懂" (didn't understand) hint back, which is what let a user who
// changed their mind be held hostage by the question they no longer meant to
// answer.
func TestApprovalWordIsNotGuessed(t *testing.T) {
	f := newFunnelFixture(t)
	f.pendingApproval(t, "")

	// Addressed to the bot, so that reaching the prompt rung means the message
	// was taken as a new request rather than ignored on the way past it.
	step, outcome, act := f.walk(t, mentioningBot(messageEvent("嗯这个嘛")))
	if step != stepPrompt || outcome != outcomeHandled || act == nil {
		t.Fatalf("walk() = (%s, %s, handoff %v), want (%s, %s) and a handoff",
			step, outcome, act != nil, stepPrompt, outcomeHandled)
	}
	if len(f.llm.approvals) != 0 {
		t.Errorf("an unrecognised reply was delivered as a decision: %+v", f.llm.approvals)
	}
	// The rung must not clear the pending permission either. Cancelling the run
	// and writing the card are runAI's job — it is the only place that sees both
	// the parked run and the new turn — and a rung that cleared the slot here
	// would leave runAI with nothing to cancel and a card still showing buttons.
	if f.sess.PendingPermission() == nil {
		t.Error("an unrecognised reply cleared the approval it did not answer")
	}
}

// TestUnrecognisedApprovalWordInAGroupIsStillOnlyARequestWhereAddressed pins the
// takeover's edge: falling through the permission rung is not the same as being
// answered, and a group message that does not mention the bot is dropped by the
// prompt rung exactly as it would be with no approval pending. Reading the reply
// as "the user wants a new turn" everywhere would make a stray line in a group
// chat cancel somebody else's parked run.
func TestUnrecognisedApprovalWordInAGroupIsStillOnlyARequestWhereAddressed(t *testing.T) {
	f := newFunnelFixture(t)
	f.pendingApproval(t, "")

	step, outcome, act := f.walk(t, messageEvent("嗯这个嘛"))
	if step != stepPrompt || outcome != outcomeDropped || act != nil {
		t.Errorf("walk() = (%s, %s, handoff %v), want (%s, %s) and no handoff",
			step, outcome, act != nil, stepPrompt, outcomeDropped)
	}
	if f.sess.PendingPermission() == nil {
		t.Error("an unaddressed group message cleared the approval it did not answer")
	}
}

// TestQuestionKindTakesFreeText is the counterweight: when the run is blocked on
// a question rather than an approval, the reply is the answer, whatever it says.
// A multi-select question is answered this way by design ("请回复选项编号", reply with the option number).
func TestQuestionKindTakesFreeText(t *testing.T) {
	f := newFunnelFixture(t)
	f.pendingApproval(t, agent.InterruptKindQuestion)

	if step, outcome, _ := f.walk(t, messageEvent("2,3")); step != stepPerm || outcome != outcomeHandled {
		t.Fatalf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepPerm, outcomeHandled)
	}
	got := f.llm.awaitApprovals(t, 1)[0]
	// The answer travels under the address of the interrupt point it answers:
	// that is what the backend resumes by, and an answer filed under the wrong
	// key would leave the question unanswered on a run that was let go.
	if got.answers["ask:1"] != "2,3" {
		t.Errorf("answer = %+v, want 2,3 filed under ask:1", got.answers)
	}
	if got.action != agent.DecisionAllow {
		t.Errorf("decision = %q, want %q: answering is not refusing", got.action, agent.DecisionAllow)
	}
}

// TestPlainTextWithNoPendingApprovalIsAPrompt pins that the permission rung only
// claims a message when something is actually waiting for one. With no
// conversation to hold a question, the word "允许" (allow) is just a word.
func TestPlainTextWithNoPendingApprovalIsAPrompt(t *testing.T) {
	f := newFunnelFixture(t)
	f.biz.sr.Sessions = nil

	// Addressed to the bot, so that reaching the prompt rung means the rung ran
	// rather than that the message was ignored on the way past it.
	step, outcome, act := f.walk(t, mentioningBot(messageEvent("允许")))
	if step != stepPrompt || outcome != outcomeHandled || act == nil {
		t.Errorf("walk() = (%s, %s, handoff %v), want (%s, %s) and a handoff",
			step, outcome, act != nil, stepPrompt, outcomeHandled)
	}
	if len(f.llm.approvals) != 0 {
		t.Error("an approval was delivered for a conversation that could not have asked")
	}
}

// TestUnaddressedGroupMessageIsNotAnswered pins the prompt rung's leaf predicate:
// a group message that does not mention the bot is ignored, and it is counted as
// ignored rather than silently returned.
func TestUnaddressedGroupMessageIsNotAnswered(t *testing.T) {
	f := newFunnelFixture(t)

	step, outcome, act := f.walk(t, messageEvent("大家早"))
	if step != stepPrompt || outcome != outcomeDropped {
		t.Errorf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepPrompt, outcomeDropped)
	}
	if act != nil {
		t.Error("an unaddressed group message was handed off anyway")
	}
}

// TestMentionedGroupMessageIsAnswered is the counterweight to the test above.
func TestMentionedGroupMessageIsAnswered(t *testing.T) {
	f := newFunnelFixture(t)

	step, outcome, act := f.walk(t, mentioningBot(messageEvent("一个问题")))
	if step != stepPrompt || outcome != outcomeHandled {
		t.Fatalf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepPrompt, outcomeHandled)
	}
	if act == nil {
		t.Error("a message addressed to the bot was not handed to the AI")
	}
}

// TestP2PIsAlwaysAnswered pins that a private conversation needs no mention.
func TestP2PIsAlwaysAnswered(t *testing.T) {
	f := newFunnelFixture(t)

	ev := messageEvent("一个问题")
	ev.Message.ChatType = im.ConvTypeP2P
	if step, outcome, act := f.walk(t, ev); step != stepPrompt || outcome != outcomeHandled || act == nil {
		t.Errorf("walk() = (%s, %s, handoff %v), want (%s, %s) and a handoff",
			step, outcome, act != nil, stepPrompt, outcomeHandled)
	}
}

// TestGroupCommandSurvivesTheMention is the routing half of the same defect the
// normalisation tests cover the text half of.
//
// In a group the bot is addressed by @-mentioning it, so the mention is attached
// to the front of "/pipeline …" when the text reaches the ladder. If the mention
// is removed after the text is trimmed — or not removed at all — the space it
// leaves behind hides the slash, the command rung declines the message, and it
// falls through to the prompt rung: the model, not the command tree, then
// decides whether to run it and with what arguments. That is a message shaping a
// build request, so the assertion is about which rung claims it.
func TestGroupCommandSurvivesTheMention(t *testing.T) {
	// Both spellings occur: the handler resolves the placeholder before the event
	// is built, but leaves it in place when the mention has no display name.
	for _, text := range []string{
		"@_user_1 /pipeline content -e edu-onex",
		"@Helper /pipeline content -e edu-onex",
	} {
		t.Run(text, func(t *testing.T) {
			f := newFunnelFixture(t)

			step, outcome, act := f.walk(t, mentioningBot(messageEvent(text)))
			if step != stepCommand || outcome != outcomeHandled {
				t.Errorf("walk() = (%s, %s), want (%s, %s): the mention took the command to the model",
					step, outcome, stepCommand, outcomeHandled)
			}
			if act == nil {
				t.Error("the command was not handed to the command tree")
			}
		})
	}
}

// TestUndecodableBodyIsDroppedAtTheContentRung keeps an unsupported payload from
// reaching either the command tree or the model as an empty question.
func TestUndecodableBodyIsDroppedAtTheContentRung(t *testing.T) {
	f := newFunnelFixture(t)

	ev := mentioningBot(messageEvent("x"))
	ev.Message.Content = im.MessageContent{Type: "text", Text: "", Raw: "not json"}

	step, outcome, act := f.walk(t, ev)
	if step != stepContent || outcome != outcomeDropped {
		t.Errorf("walk() = (%s, %s), want (%s, %s)", step, outcome, stepContent, outcomeDropped)
	}
	if act != nil {
		t.Error("an undecodable body was handed off anyway")
	}
}
