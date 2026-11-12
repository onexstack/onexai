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
	"strings"
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/roleset"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
)

// The "切换 Agent" (switch Agent) card is the second way to change a conversation's
// declaration, and after State.AgentName was retired it is the only one that is
// not typed. It has to reach the same column, under the same policy, as the
// /agent command — and say the same thing about the result, which is what the
// second half of these tests is about.

const (
	cardApp    = "cli_example"
	cardGroup  = "oc_group_1"
	cardAdmin  = "ou_admin"
	cardMember = "ou_member"
)

// listingAgents is a client that answers ListAgent, which is all the agent card
// asks oai-agent-apiserver for.
type listingAgents struct {
	agentclient.Interface
	agents []*agentv1.Agent
}

func (c *listingAgents) ListAgent(context.Context) ([]*agentv1.Agent, error) {
	return c.agents, nil
}

// agentCardFixture is a deployment whose agent list is known, whose session
// writes are recorded, and whose peer bindings are the caller's to fill in.
func agentCardFixture(t *testing.T, admins ...string) (*botBiz, *fakeSessionStore, *fakePeerAgentStore) {
	t.Helper()

	adminSet := map[string]struct{}{}
	for _, a := range admins {
		adminSet[a] = struct{}{}
	}

	session := &fakeSessionStore{}
	peers := &fakePeerAgentStore{rows: map[string]*model.PeerAgentM{}}
	b := &botBiz{sr: &runtime.StaticRuntime{
		Store:                &fakeStore{session: session, peers: peers},
		Roles:                roleset.NewStatic(nil, adminSet),
		AppID:                cardApp,
		InternalDefaultAgent: "helper",
		Clientset: &fakeClientset{llm: &listingAgents{agents: []*agentv1.Agent{
			{Name: "ops-agent", DisplayName: "Ops"},
			{Name: "bke-agent", DisplayName: "BKE"},
		}}},
	}}
	b.cardNav = b.defaultCardNavHandler
	return b, session, peers
}

// bind gives the fixture a binding for one open_id under this deployment's app.
func (p *fakePeerAgentStore) bind(peerID, canonical string, agents ...string) {
	p.rows["lark|"+cardApp+"|"+peerID] = &model.PeerAgentM{
		Backend: "lark", Scope: cardApp, PeerID: peerID,
		Canonical: canonical, Agents: model.EncodeNameList(agents),
	}
}

// inGroup gives the conversation a session row declaring refs, which is what a
// card press reads to know what it is showing.
func declareRefs(s *fakeSessionStore, refs ...string) {
	s.getFn = func(context.Context, *where.Options) (*model.SessionM, error) {
		return &model.SessionM{
			SessionID:   cardGroup,
			SessionType: model.SessionTypePrivateChat,
			Refs:        model.EncodeNameList(refs),
		}, nil
	}
}

// press builds the card-action event a button press delivers.
//
// The button's value carries the session key, which is what the card renderer
// injects and what a click is addressed by; the operator carries the two
// identities of whoever pressed. The open_id is on the event and nowhere else,
// which is the reason the event travels down to the card at all.
func press(chatID, senderID, openID string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: openID, UserID: ptr.To(senderID)},
			Action:   &callback.CallBackAction{Value: map[string]interface{}{"session_key": chatID}},
		},
	}
}

// cardBody is every markdown line the card carries, joined. The assertions are
// about what a reader sees, and the card builds its body as several lines.
func cardBody(card *larkcard.Card) string {
	if card == nil {
		return ""
	}
	var b strings.Builder
	for _, el := range card.Elements {
		if md, ok := el.(larkcard.CardMarkdown); ok {
			b.WriteString(md.Content)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ---- the write ----

// TestTheAgentCardWritesTheDeclaration pins the write itself. The card records
// the same column /agent does, because it is a second way to reach one action
// and not a second policy.
func TestTheAgentCardWritesTheDeclaration(t *testing.T) {
	b, session, _ := agentCardFixture(t, cardAdmin)

	b.selectAgent(context.Background(), press(cardGroup, cardAdmin, "ou_admin_open"), recvFor(cardAdmin), "ops-agent")

	if len(session.refs) != 1 || session.refs[0] != `oc_group_1=["ops-agent"]` {
		t.Fatalf("declaration writes = %v, want one naming the session and the agent", session.refs)
	}
}

// TestTheAgentCardCanClearTheDeclaration pins the empty selection. Clearing is
// not the same as naming the default: an empty column keeps following the
// conversation type's template, while `["@default"]` freezes today's answer.
func TestTheAgentCardCanClearTheDeclaration(t *testing.T) {
	b, session, _ := agentCardFixture(t, cardAdmin)

	b.selectAgent(context.Background(), press(cardGroup, cardAdmin, "ou_admin_open"), recvFor(cardAdmin), "default")

	if len(session.refs) != 1 || session.refs[0] != "oc_group_1=" {
		t.Errorf("declaration writes = %v, want one clearing the column", session.refs)
	}
}

// ---- who may write ----

// TestTheAgentCardIsAdminOnly pins the gate on this path too. The card carries
// the caller's identity, and without the check any member of a group could
// point the conversation at an agent of their choosing by pressing a button —
// the same exposure the command's check exists for.
func TestTheAgentCardIsAdminOnly(t *testing.T) {
	b, session, _ := agentCardFixture(t, cardAdmin)

	card := b.selectAgent(context.Background(), press(cardGroup, cardMember, "ou_member_open"), recvFor(cardMember), "ops-agent")

	if len(session.refs) != 0 {
		t.Errorf("a non-admin's press wrote %v", session.refs)
	}
	if card == nil {
		t.Fatal("a refused press returned no card: the person gets no feedback at all")
	}
}

// TestTheAgentCardRefusesANameThatWouldShadowAReference pins the check the card
// shares with /agent. An agent named `@peer` would be unreachable — the
// scheduler reads that string as the peer reference — so the name is refused
// where it is written, and nothing is stored.
func TestTheAgentCardRefusesANameThatWouldShadowAReference(t *testing.T) {
	b, session, _ := agentCardFixture(t, cardAdmin)

	card := b.selectAgent(context.Background(), press(cardGroup, cardAdmin, "ou_admin_open"), recvFor(cardAdmin), "@peer")

	if len(session.refs) != 0 {
		t.Errorf("a shadowing name wrote %v", session.refs)
	}
	if card == nil {
		t.Fatal("a refused name returned no card")
	}
}

// ---- what the declaration resolves to ----

// TestTheAgentCardShowsWhatTheDeclarationResolvesTo pins the half a declaration
// cannot express on its own.
//
// `@peer` is stored as five characters and means different things to different
// people; the card is where the person who set it finds out which. Without this
// the card would show the intent and leave the outcome to the next answer —
// which is one message too late to be useful, because by then the mistake is in
// the conversation's state rather than in the reply being read.
func TestTheAgentCardShowsWhatTheDeclarationResolvesTo(t *testing.T) {
	b, session, peers := agentCardFixture(t, cardAdmin)
	declareRefs(session, "@peer")
	peers.bind("ou_alice_open", "user-alice", "onex-u-user-alice")

	card := b.renderAgentCard(context.Background(), press(cardGroup, cardAdmin, "ou_alice_open"), recvFor(cardAdmin))

	if body := cardBody(card); !strings.Contains(body, "onex-u-user-alice") {
		t.Errorf("card body = %q, want it to name the agent the declaration picks", body)
	}
}

// TestTheAgentCardResolvesForThePersonWhoPressed is the property the raw event
// had to be threaded down for.
//
// Two people press the same button in the same conversation, which declares
// `@peer`. They are bound to different agents, so the card has to say two
// different things — and the only thing distinguishing them on this path is the
// open_id, which lives on the card event and on no other input the handler gets.
func TestTheAgentCardResolvesForThePersonWhoPressed(t *testing.T) {
	b, session, peers := agentCardFixture(t, cardAdmin, cardMember)
	declareRefs(session, "@peer")
	peers.bind("ou_admin_open", "user-admin", "onex-u-user-admin")
	peers.bind("ou_member_open", "user-member", "onex-u-user-member")

	adminBody := cardBody(b.renderAgentCard(context.Background(),
		press(cardGroup, cardAdmin, "ou_admin_open"), recvFor(cardAdmin)))
	memberBody := cardBody(b.renderAgentCard(context.Background(),
		press(cardGroup, cardMember, "ou_member_open"), recvFor(cardMember)))

	if !strings.Contains(adminBody, "onex-u-user-admin") {
		t.Errorf("the admin's card = %q, want their own agent", adminBody)
	}
	if !strings.Contains(memberBody, "onex-u-user-member") {
		t.Errorf("the member's card = %q, want their own agent", memberBody)
	}
	if adminBody == memberBody {
		t.Error("the same card was rendered for two speakers with different bindings")
	}
}

// TestTheAgentCardSaysWhenNothingResolves pins the other half. A declaration
// that picks nothing is the case the person most needs told about, and the
// sentence is the one a turn with no agent shows — the reader has the same
// things to do about it either way.
func TestTheAgentCardSaysWhenNothingResolves(t *testing.T) {
	b, session, _ := agentCardFixture(t, cardAdmin)
	declareRefs(session, "@peer")

	// Nobody pressed anything that has a binding.
	card := b.renderAgentCard(context.Background(), press(cardGroup, cardAdmin, "ou_nobody_open"), recvFor(cardAdmin))

	if body := cardBody(card); !strings.Contains(body, "绑定") {
		t.Errorf("card body = %q, want the invitation to bind", body)
	}
}

// TestTheAgentCardStillRendersWithoutAnEvent pins the fallback. A caller that
// renders this card with no card event behind it — a test, or an entry point
// that has not been written yet — gets the declaration and no resolution, rather
// than a wrong resolution or a panic.
func TestTheAgentCardStillRendersWithoutAnEvent(t *testing.T) {
	b, session, _ := agentCardFixture(t, cardAdmin)
	declareRefs(session, "@peer")

	card := b.renderAgentCard(context.Background(), nil, recvFor(cardAdmin))

	if card == nil {
		t.Fatal("renderAgentCard returned no card without an event")
	}
	body := cardBody(card)
	if !strings.Contains(body, "@peer") {
		t.Errorf("card body = %q, want the declaration shown", body)
	}
	if strings.Contains(body, "解析") {
		t.Errorf("card body = %q, want no resolution line without an event to resolve against", body)
	}
}

// recvFor is the receiver the card handlers are given, matching what
// parseReceiverFromCardEvent derives from press().
func recvFor(senderID string) *v1.Receiver {
	return &v1.Receiver{Id: cardGroup, Type: v1.ReceiverTypeChatID}
}
