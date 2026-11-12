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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// scheduled builds a bot over a recording session store, with a conversation
// that already has a row.
func scheduled(t *testing.T, defaults AudienceDefaults, refs []string) (*botBiz, *fakeSessionStore, *runtime.DynamicRuntime) {
	t.Helper()

	session := &fakeSessionStore{}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Store:                &fakeStore{session: session},
		InternalDefaultAgent: defaults.Internal,
		ExternalDefaultAgent: defaults.External,
	}}
	dr := &runtime.DynamicRuntime{
		Sender: v1.NewSender("go.kong", ""), Receiver: &v1.Receiver{Id: "oc_group_1"},
		SessionM: &model.SessionM{
			SessionID:   "oc_group_1",
			SessionType: model.SessionTypeNormalGroup,
			Refs:        model.EncodeNameList(refs),
		},
	}
	return b, session, dr
}

// TestScheduleReportsTheDecisionAndWritesNothing is the whole contract of a
// scheduling pass, in both halves.
//
// The first half is the answer every reader consumes: the agents, the identity
// to run as, the declaration they came from, and the reason when there are none.
//
// The second half is what it must **not** do. The result used to be recorded on
// the conversation's row (chat.session.agents) and read back by whoever rendered
// a menu; that column is gone, because a resolution belongs to a speaker rather
// than to a room — `@peer` is one agent for whoever bound an account and another
// for the next person to talk. So nothing is written here at all, and this
// asserts both columns are left alone.
func TestScheduleReportsTheDecisionAndWritesNothing(t *testing.T) {
	b, session, dr := scheduled(t, AudienceDefaults{Internal: "helper"}, nil)

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}

	if len(result.Agents) != 1 || result.Agents[0] != "helper" {
		t.Errorf("Agents = %v, want the configured default", result.Agents)
	}
	if result.Subject != "go.kong" {
		t.Errorf("Subject = %q, want the tenant identity: an unbound speaker runs as themselves", result.Subject)
	}
	if result.Hint != HintNone {
		t.Errorf("Hint = %q, want none for a turn that has an agent", result.Hint)
	}
	// The declaration travels with the answer: an empty one means the
	// conversation type's template applied, and only the scheduler knows that.
	if got := strings.Join(result.Refs, ","); got != "@peer,@default" {
		t.Errorf("Refs = %q, want the normal-group template the empty declaration falls back to", got)
	}

	if len(session.refs) != 0 {
		t.Errorf("the scheduler wrote refs (%v), want the declaration left to /agent", session.refs)
	}
	if dr.SessionM.Refs != "" {
		t.Errorf("the snapshot's Refs = %q, want it untouched", dr.SessionM.Refs)
	}
}

// TestScheduleReportsNothingResolved pins the empty case. It has to report the
// reason as well as the emptiness: "no agent" sends the reader to an
// administrator, while "you are not bound" is something they can fix themselves.
func TestScheduleReportsNothingResolved(t *testing.T) {
	// An internal speaker who asked for their own agent and has no binding: the
	// case the reply invites them to fix.
	b, _, dr := scheduled(t, AudienceDefaults{Internal: "helper"}, []string{"@peer"})

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}

	if len(result.Agents) != 0 {
		t.Errorf("Agents = %v, want nothing resolved", result.Agents)
	}
	if result.Hint != HintUnbound {
		t.Errorf("Hint = %q, want the invitation to bind", result.Hint)
	}
}

// TestScheduleIsSilentWhenThereIsNoRowToWriteTo pins that a conversation the
// store has no row for yet still gets a decision: there is no row to read a
// declaration from, so the conversation type's template applies.
func TestScheduleIsSilentWhenThereIsNoRowToWriteTo(t *testing.T) {
	b := &botBiz{sr: &runtime.StaticRuntime{InternalDefaultAgent: "helper"}}
	dr := &runtime.DynamicRuntime{
		Sender: v1.NewSender("go.kong", ""), Receiver: &v1.Receiver{Id: "oc_group_1"},
		// No SessionM: the row failed to persist, or this message arrived first.
	}

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}
	if len(result.Agents) != 1 || result.Agents[0] != "helper" {
		t.Errorf("Agents = %v, want the template's default", result.Agents)
	}
	if result.Hint != HintNone {
		t.Errorf("Hint = %q, want none: the row is missing, the answer is not", result.Hint)
	}
}

// boundSpeaker wires a conversation whose speaker has a binding for the app the
// deployment is configured with.
//
// The scope is `b.sr.AppID`, so a test whose lookup misses is a test that used
// the wrong scope — which is the mistake this arrangement exists to make loud:
// the scope comes from configuration, not from anything on the message.
func boundSpeaker(t *testing.T, backend, appID, peerID, canonical string, agents []string) (*botBiz, *fakeSessionStore, *runtime.DynamicRuntime) {
	t.Helper()

	peers := &fakePeerAgentStore{rows: map[string]*model.PeerAgentM{
		backend + "|" + appID + "|" + peerID: {
			Backend: backend, Scope: appID, PeerID: peerID,
			Canonical: canonical,
			Agents:    model.EncodeNameList(agents),
		},
	}}

	session := &fakeSessionStore{}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Store: &fakeStore{session: session, peers: peers},
		AppID: appID,
	}}
	dr := &runtime.DynamicRuntime{
		// A tenant identity and an app identity for the same person. They are
		// deliberately different strings: a binding that is found must produce
		// neither of them as the subject.
		Sender:   v1.NewSender("go.kong", peerID),
		Receiver: &v1.Receiver{Id: "oc_group_1"},
		SessionM: &model.SessionM{
			SessionID:   "oc_group_1",
			SessionType: model.SessionTypePrivateChat,
			Refs:        model.EncodeNameList([]string{"@peer"}),
		},
	}
	return b, session, dr
}

// TestASpeakerRunsAsTheirBindingsSubject is the normalization half of the design
// (§5), and the reason the two call paths into the agent service can agree.
//
// ox-agent-apiserver sends OneX's own userID as the subject; chat only ever
// holds an open_id. The binding is what translates one into the other, so the
// value asserted here is neither of the two chat identifiers — it is the third
// thing, and getting it wrong is silent: the authorization simply answers about
// a different identity.
func TestASpeakerRunsAsTheirBindingsSubject(t *testing.T) {
	b, _, dr := boundSpeaker(t, "lark", "cli_app", "ou_alice", "user-alice", []string{"onex-u-user-alice"})

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}
	if result.Subject != "user-alice" {
		t.Errorf("Subject = %q, want the binding's canonical identity", result.Subject)
	}
	if len(result.Agents) != 1 || result.Agents[0] != "onex-u-user-alice" {
		t.Errorf("Agents = %v, want the agents the binding names", result.Agents)
	}
	// The two chat identities must not have leaked into the subject. Named
	// individually because "not user-alice" would pass on a bug that used the
	// other one.
	if result.Subject == dr.Handle() {
		t.Error("Subject is the open_id: it is what the binding is looked up *by*, and is never sent onward")
	}
	if result.Subject == dr.Identity() {
		t.Error("Subject is the tenant user_id: correct only when there is no binding")
	}
}

// TestABindingWithoutACanonicalFallsBackToTheTenantIdentity pins the half-filled
// row. It is a state the writer is not supposed to produce, and if one exists,
// running as the tenant identity is the behaviour that matches an unbound
// speaker — rather than sending the agent service an empty subject.
func TestABindingWithoutACanonicalFallsBackToTheTenantIdentity(t *testing.T) {
	b, _, dr := boundSpeaker(t, "lark", "cli_app", "ou_alice", "", []string{"onex-u-user-alice"})

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}
	if result.Subject != "go.kong" {
		t.Errorf("Subject = %q, want the tenant identity", result.Subject)
	}
	// The agents still come from the row: the identity and the agent list are
	// two facts from one row, and a missing one does not invalidate the other.
	if len(result.Agents) != 1 {
		t.Errorf("Agents = %v, want the row's agents", result.Agents)
	}
}

// TestAFailedBindingLookupRefusesTheTurn pins that a lookup error is not read as
// "not bound". Answering as unbound would invite a user who is bound to bind
// again, and would run their turn as somebody else.
//
// The error is returned rather than turned into an empty answer here; what a
// caller does with it is the caller's decision, and the two readers want
// different things from it (a command explains, a menu renders nothing).
func TestAFailedBindingLookupRefusesTheTurn(t *testing.T) {
	b, _, dr := boundSpeaker(t, "lark", "cli_app", "ou_alice", "user-alice", nil)
	b.sr.Store.(*fakeStore).peers = &fakePeerAgentStore{err: errors.New("the binding store is unavailable")}

	if _, err := b.schedule(context.Background(), dr); err == nil {
		t.Fatal("schedule() answered a turn whose binding lookup failed")
	}

	// The callback the commands consume reports it too, rather than dressing a
	// failed lookup up as an empty agent list.
	if _, err := b.resolvedAgents(context.Background(), dr); err == nil {
		t.Fatal("resolvedAgents() reported an answer for a lookup that failed")
	}
}

// TestResolvedAgentsOnceReadsOncePerRequest pins the scope of the memo that
// wraps the pass for every request the bot builds a runtime for.
//
// One command request asks the question twice — assembling the tree, and the
// gate that runs before the command does — and the answer cannot differ between
// the two: same caller, same row, same binding. The read is the whole cost of
// resolving, so the count is the assertion.
func TestResolvedAgentsOnceReadsOncePerRequest(t *testing.T) {
	b, _, dr := boundSpeaker(t, "lark", "cli_app", "ou_alice", "user-alice", []string{"onex-u-user-alice"})
	peers := b.sr.Store.(*fakeStore).peers.(*fakePeerAgentStore)

	resolve := b.resolvedAgentsOnce()
	for i := range 2 {
		res, err := resolve(context.Background(), dr)
		if err != nil {
			t.Fatalf("resolve %d = %v", i+1, err)
		}
		if len(res.Agents) != 1 || res.Agents[0] != "onex-u-user-alice" {
			t.Errorf("resolve %d = %v, want the binding's agents both times", i+1, res.Agents)
		}
	}
	if peers.gets != 1 {
		t.Errorf("the binding was read %d times, want once per request", peers.gets)
	}

	// A second request must do its own read: the memo is scoped to one request's
	// runtime, not to the bot. A cache here would answer the next person in a
	// group with the previous person's agent, which is the bug the recorded
	// column was deleted for.
	if _, err := b.resolvedAgentsOnce()(context.Background(), dr); err != nil {
		t.Fatalf("second request = %v", err)
	}
	if peers.gets != 2 {
		t.Errorf("the binding was read %d times over two requests, want one each", peers.gets)
	}
}

// TestResolvedAgentsReportsWhatTheDeclarationPicks pins the answer /agent and
// /chat both render.
//
// It is the answer to a question a command cannot work out itself: it wrote an
// intent, and only the scheduler knows whether that intent picks anything — and
// for whom. The empty case carries the notice a turn with no agent shows,
// because the reader has the same three things to do about it.
func TestResolvedAgentsReportsWhatTheDeclarationPicks(t *testing.T) {
	t.Run("a declaration that picks an agent", func(t *testing.T) {
		b, _, dr := boundSpeaker(t, "lark", "cli_app", "ou_alice", "user-alice", []string{"onex-u-user-alice"})

		res, err := b.resolvedAgents(context.Background(), dr)
		if err != nil {
			t.Fatalf("resolvedAgents() = %v", err)
		}
		if len(res.Agents) != 1 || res.Agents[0] != "onex-u-user-alice" {
			t.Errorf("Agents = %v, want the agents the binding names", res.Agents)
		}
		if !strings.Contains(res.Line(), "onex-u-user-alice") {
			t.Errorf("Line() = %q, want it to name the agent the declaration picks", res.Line())
		}
	})

	t.Run("a declaration that picks nothing", func(t *testing.T) {
		// An unbound speaker asking for their own agent, which is the case the
		// person who typed `/agent @peer` most needs told about.
		session := &fakeSessionStore{}
		b := &botBiz{sr: &runtime.StaticRuntime{
			Store: &fakeStore{session: session, peers: &fakePeerAgentStore{}},
		}}
		dr := &runtime.DynamicRuntime{
			Sender: v1.NewSender("go.kong", ""), Receiver: &v1.Receiver{Id: "oc_group_1"},
			SessionM: &model.SessionM{
				SessionID: "oc_group_1", Refs: model.EncodeNameList([]string{"@peer"}),
			},
		}

		res, err := b.resolvedAgents(context.Background(), dr)
		if err != nil {
			t.Fatalf("resolvedAgents() = %v", err)
		}
		if res.Hint != runtime.AgentHintUnbound {
			t.Errorf("Hint = %q, want the invitation to bind", res.Hint)
		}
		if !strings.Contains(res.Line(), "绑定") {
			t.Errorf("Line() = %q, want the invitation to bind", res.Line())
		}
	})

	t.Run("a declaration that cannot be resolved at all", func(t *testing.T) {
		b, _, dr := boundSpeaker(t, "lark", "cli_app", "ou_alice", "user-alice", nil)
		dr.SessionM.Refs = model.EncodeNameList([]string{"@nope"})

		// Reported as an error rather than as an empty answer: the two are told
		// apart by the caller, because "nothing resolved" has a next step and
		// "we could not tell" has none.
		if _, err := b.resolvedAgents(context.Background(), dr); err == nil {
			t.Error("resolvedAgents() answered for a reference no decider claims")
		}
	})
}

// privateChat wires a P2P conversation whose speaker may or may not have a
// binding. agents == nil means "no binding row"; an empty slice means "a row
// that names no agent" — two states the scheduler must tell apart.
func privateChat(
	t *testing.T, refs []string, agents []string, canonical string,
) (*botBiz, *fakeSessionStore, *runtime.DynamicRuntime) {
	t.Helper()

	peers := &fakePeerAgentStore{rows: map[string]*model.PeerAgentM{}}
	if agents != nil {
		peers.rows["lark|cli_app|ou_alice"] = &model.PeerAgentM{
			Backend: "lark", Scope: "cli_app", PeerID: "ou_alice",
			Canonical: canonical, Agents: model.EncodeNameList(agents),
		}
	}

	session := &fakeSessionStore{}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Store:                &fakeStore{session: session, peers: peers},
		AppID:                "cli_app",
		InternalDefaultAgent: "helper",
	}}
	dr := &runtime.DynamicRuntime{
		Sender:   v1.NewSender("go.kong", "ou_alice"),
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeUserID, Id: "go.kong"},
		SessionM: &model.SessionM{
			SessionID:   "go.kong",
			ChatID:      "go.kong",
			SessionType: model.SessionTypePrivateChat,
			Refs:        model.EncodeNameList(refs),
		},
	}
	return b, session, dr
}

// TestPrivateChatBindingOutranksTheSessionDeclaration is the defect this change
// fixes: a declaration written earlier by /agent must not silently shadow the
// binding the console shows and the console promises.
func TestPrivateChatBindingOutranksTheSessionDeclaration(t *testing.T) {
	b, session, dr := privateChat(t, []string{"fz-public"}, []string{"fz-admin"}, "user-admin")

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}
	if len(result.Agents) != 1 || result.Agents[0] != "fz-admin" {
		t.Errorf("Agents = %v, want the binding's agent", result.Agents)
	}
	if len(result.Refs) != 1 || result.Refs[0] != "@peer" {
		t.Errorf("Refs = %v, want the binding's reference alone", result.Refs)
	}
	if !result.BindingPreferred {
		t.Error("BindingPreferred = false, want true: the binding chose the refs")
	}
	if result.Subject != "user-admin" {
		t.Errorf("Subject = %q, want the binding's canonical identity", result.Subject)
	}
	if len(session.refs) != 0 {
		t.Errorf("the pass wrote session.refs: %v; scheduling is read-only", session.refs)
	}
	// The stored declaration is history, not truth; it must not be rewritten.
	if got := dr.SessionM.RefList(); len(got) != 1 || got[0] != "fz-public" {
		t.Errorf("the in-memory declaration changed: %v", got)
	}
}

// TestPrivateChatFallsBackToTheDeclarationWhenTheBindingNamesNothing pins that
// an empty binding is a deliberate state, not an override. It falls back to the
// declaration rather than replacing it with nothing.
func TestPrivateChatFallsBackToTheDeclarationWhenTheBindingNamesNothing(t *testing.T) {
	tests := []struct {
		name   string
		agents []string
	}{
		{"bound with an empty list", []string{}},
		{"no binding row", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _, dr := privateChat(t, []string{"fz-public"}, tt.agents, "")

			result, err := b.schedule(context.Background(), dr)
			if err != nil {
				t.Fatalf("schedule() = %v", err)
			}
			if len(result.Agents) != 1 || result.Agents[0] != "fz-public" {
				t.Errorf("Agents = %v, want the conversation's declaration", result.Agents)
			}
			if result.BindingPreferred {
				t.Error("BindingPreferred = true, want false: the binding named no agent")
			}
		})
	}
}

// TestPrivateChatWithoutADeclarationUsesTheTemplate pins that an unbound
// private chat still falls through @peer to the platform default.
func TestPrivateChatWithoutADeclarationUsesTheTemplate(t *testing.T) {
	b, _, dr := privateChat(t, nil, nil, "")

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}
	if len(result.Refs) != 2 || result.Refs[0] != "@peer" || result.Refs[1] != "@default" {
		t.Errorf("Refs = %v, want the private-chat template", result.Refs)
	}
	if len(result.Agents) != 1 || result.Agents[0] != "helper" {
		t.Errorf("Agents = %v, want the platform default", result.Agents)
	}
}

// TestPrivateChatBoundButEmptyUsesTheTemplate pins the other half of the empty
// binding: with no declaration either, the template still applies in full.
func TestPrivateChatBoundButEmptyUsesTheTemplate(t *testing.T) {
	b, _, dr := privateChat(t, nil, []string{}, "")

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}
	if len(result.Refs) != 2 || result.Refs[0] != "@peer" || result.Refs[1] != "@default" {
		t.Errorf("Refs = %v, want the private-chat template", result.Refs)
	}
	if result.BindingPreferred {
		t.Error("BindingPreferred = true, want false")
	}
}

// TestGroupKeepsTheDeclarationOverTheBinding is the regression guard for the
// other half of the rule: a group's declaration is a room-level decision and
// must not be replaced by whichever member happens to speak.
func TestGroupKeepsTheDeclarationOverTheBinding(t *testing.T) {
	peers := &fakePeerAgentStore{rows: map[string]*model.PeerAgentM{
		"lark|cli_app|ou_alice": {
			Backend: "lark", Scope: "cli_app", PeerID: "ou_alice",
			Canonical: "user-alice", Agents: model.EncodeNameList([]string{"fz-admin"}),
		},
	}}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Store:                &fakeStore{session: &fakeSessionStore{}, peers: peers},
		AppID:                "cli_app",
		InternalDefaultAgent: "helper",
	}}
	dr := &runtime.DynamicRuntime{
		Sender:   v1.NewSender("go.kong", "ou_alice"),
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_group_1"},
		SessionM: &model.SessionM{
			SessionID:   "oc_group_1",
			SessionType: model.SessionTypeNormalGroup,
			Refs:        model.EncodeNameList([]string{"ops-agent"}),
		},
	}

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}
	if len(result.Agents) != 1 || result.Agents[0] != "ops-agent" {
		t.Errorf("Agents = %v, want the group's declaration", result.Agents)
	}
	if result.BindingPreferred {
		t.Error("BindingPreferred = true, want false: groups keep the declaration")
	}
}

// TestMissingSessionOnAP2PInfersPrivateChat fixes the second half of the same
// bug: without a row the old code fell into the catch-all template and never
// consulted @peer, so a bound user's DM ignored their binding. A direct
// receiver is enough to identify the conversation type.
func TestMissingSessionOnAP2PInfersPrivateChat(t *testing.T) {
	t.Run("unbound falls through the template to the default", func(t *testing.T) {
		b := &botBiz{sr: &runtime.StaticRuntime{
			Store:                &fakeStore{session: &fakeSessionStore{}, peers: &fakePeerAgentStore{}},
			AppID:                "cli_app",
			InternalDefaultAgent: "helper",
		}}
		dr := &runtime.DynamicRuntime{
			Sender:   v1.NewSender("go.kong", "ou_alice"),
			Receiver: &v1.Receiver{Type: v1.ReceiverTypeUserID, Id: "go.kong"},
		}

		result, err := b.schedule(context.Background(), dr)
		if err != nil {
			t.Fatalf("schedule() = %v", err)
		}
		if len(result.Refs) != 2 || result.Refs[0] != "@peer" || result.Refs[1] != "@default" {
			t.Errorf("Refs = %v, want the private-chat template", result.Refs)
		}
		if len(result.Agents) != 1 || result.Agents[0] != "helper" {
			t.Errorf("Agents = %v, want the platform default", result.Agents)
		}
	})

	t.Run("bound resolves the binding", func(t *testing.T) {
		peers := &fakePeerAgentStore{rows: map[string]*model.PeerAgentM{
			"lark|cli_app|ou_alice": {
				Backend: "lark", Scope: "cli_app", PeerID: "ou_alice",
				Canonical: "user-alice", Agents: model.EncodeNameList([]string{"fz-admin"}),
			},
		}}
		b := &botBiz{sr: &runtime.StaticRuntime{
			Store:                &fakeStore{session: &fakeSessionStore{}, peers: peers},
			AppID:                "cli_app",
			InternalDefaultAgent: "helper",
		}}
		dr := &runtime.DynamicRuntime{
			Sender:   v1.NewSender("go.kong", "ou_alice"),
			Receiver: &v1.Receiver{Type: v1.ReceiverTypeUserID, Id: "go.kong"},
		}

		result, err := b.schedule(context.Background(), dr)
		if err != nil {
			t.Fatalf("schedule() = %v", err)
		}
		if len(result.Agents) != 1 || result.Agents[0] != "fz-admin" {
			t.Errorf("Agents = %v, want the binding's agent", result.Agents)
		}
	})
}

// TestOpenAPIConversationIsUnaffected pins that a REST caller — no platform
// handle, no binding — keeps the template it had.
func TestOpenAPIConversationIsUnaffected(t *testing.T) {
	b := &botBiz{sr: &runtime.StaticRuntime{
		Store:                &fakeStore{session: &fakeSessionStore{}, peers: &fakePeerAgentStore{}},
		AppID:                "cli_app",
		InternalDefaultAgent: "helper",
	}}
	dr := &runtime.DynamicRuntime{
		Sender:   v1.NewSender("user-1", ""),
		Receiver: &v1.Receiver{Type: "api"},
		SessionM: &model.SessionM{SessionID: "conv-1", SessionType: model.SessionTypeOpenAPI},
	}

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}
	if len(result.Agents) != 1 || result.Agents[0] != "helper" {
		t.Errorf("Agents = %v, want the platform default", result.Agents)
	}
	if result.BindingPreferred {
		t.Error("BindingPreferred = true, want false")
	}
}

// ticketDuty answers GetGroupMeta with one group and leaves the rest of the
// duty service to the embedded interface.
type ticketDuty struct {
	duty.Service
	group duty.Group
}

func (d ticketDuty) GetGroupMeta(context.Context, string) (duty.Group, bool, error) {
	return d.group, true, nil
}

// TestTicketGroupPrefersTheOncallAgent pins the priority a ticket group exists
// for: the agent configured on its duty group runs, even when the speaker has a
// personal binding. The binding is only the fallback for a group that configures
// no agent.
func TestTicketGroupPrefersTheOncallAgent(t *testing.T) {
	peers := &fakePeerAgentStore{rows: map[string]*model.PeerAgentM{
		"lark|cli_app|ou_alice": {
			Backend: "lark", Scope: "cli_app", PeerID: "ou_alice",
			Agents: model.EncodeNameList([]string{"fz-admin"}),
		},
	}}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Store:                &fakeStore{session: &fakeSessionStore{}, peers: peers},
		AppID:                "cli_app",
		InternalDefaultAgent: "helper",
		Duty:                 ticketDuty{group: duty.Group{ID: "cc", Agents: []string{"agent"}}},
	}}
	dr := &runtime.DynamicRuntime{
		Sender:   v1.NewSender("go.kong", "ou_alice"),
		Receiver: &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: "oc_cc"},
		SessionM: &model.SessionM{
			SessionID:   "oc_cc",
			ChatID:      "oc_cc",
			SessionType: model.SessionTypeTicketGroup,
		},
		TicketM: &model.TicketM{GroupID: "cc"},
	}

	result, err := b.schedule(context.Background(), dr)
	if err != nil {
		t.Fatalf("schedule() = %v", err)
	}
	if len(result.Agents) != 1 || result.Agents[0] != "agent" {
		t.Errorf("Agents = %v, want the on-call group's agent", result.Agents)
	}
	if got := strings.Join(result.Refs, ","); got != "@oncall,@peer,@default" {
		t.Errorf("Refs = %q, want the ticket-group template", got)
	}
}
