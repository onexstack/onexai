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
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	"time"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/store"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// fakeSessionStore records the session passed to Create/Delete so tests can
// assert the persisted fields and the delete target. Get returns ErrRecordNotFound
// by default, or a caller-supplied result via getFn.
type fakeSessionStore struct {
	created *model.SessionM
	deleted *where.Options
	getFn   func(context.Context, *where.Options) (*model.SessionM, error)
	// refs records the column-level writes, in order, as "sessionID=value" — the
	// only column a read path still writes (the resolved result is not recorded
	// at all any more).
	refs []string
}

func (s *fakeSessionStore) Create(_ context.Context, obj *model.SessionM) error {
	s.created = obj
	return nil
}

func (s *fakeSessionStore) Update(context.Context, *model.SessionM) error { return nil }
func (s *fakeSessionStore) SetRefs(_ context.Context, sessionID, refs string) error {
	s.refs = append(s.refs, sessionID+"="+refs)
	return nil
}

// ListDismissable and MarkDismissed are the dissolution sweep's; this fake is
// not used by it.
func (s *fakeSessionStore) ListDismissable(context.Context, time.Time, int) ([]*model.SessionM, error) {
	return nil, nil
}

func (s *fakeSessionStore) MarkDismissed(context.Context, string, time.Time) error { return nil }
func (s *fakeSessionStore) Delete(_ context.Context, opts *where.Options) error {
	s.deleted = opts
	return nil
}
func (s *fakeSessionStore) Get(ctx context.Context, opts *where.Options) (*model.SessionM, error) {
	if s.getFn != nil {
		return s.getFn(ctx, opts)
	}
	return nil, store.ErrRecordNotFound
}
func (s *fakeSessionStore) CountByGroup(context.Context, string) (int64, error) { return 0, nil }

func (s *fakeSessionStore) List(context.Context, *where.Options) (int64, []*model.SessionM, error) {
	return 0, nil, nil
}

// fakePeerAgentStore serves bindings out of a map keyed by the triple. It is
// where "this speaker has bound an account" comes from in the scheduling tests.
type fakePeerAgentStore struct {
	store.PeerAgentStore
	rows map[string]*model.PeerAgentM
	// err, when set, is what Get reports — the case a turn must refuse rather
	// than answer as somebody else.
	err error
	// gets counts lookups, which is how the per-request memo is asserted: the
	// read is the whole cost of resolving, so "how many" is the observable.
	gets int
}

func (s *fakePeerAgentStore) GetByPeer(_ context.Context, backend, scope, peerID string) (*model.PeerAgentM, error) {
	s.gets++
	if s.err != nil {
		return nil, s.err
	}
	return s.rows[backend+"|"+scope+"|"+peerID], nil
}

func (s *fakePeerAgentStore) Put(context.Context, *model.PeerAgentM) error { return nil }

func (s *fakePeerAgentStore) DeleteByPeer(context.Context, string, string, string) error { return nil }

// fakeStore implements store.IStore with only the session store wired; the other
// sub-stores are never touched by ProcessChatMemberBotAdded.
type fakeStore struct {
	session *fakeSessionStore
	// peers is nil unless a test needs a binding; a nil PeerAgentStore would
	// panic, which is the right failure for a test that forgot to wire one.
	peers store.PeerAgentStore
}

func (s *fakeStore) DB(context.Context, ...where.Where) *gorm.DB { return nil }
func (s *fakeStore) TX(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (s *fakeStore) Session() store.SessionStore              { return s.session }
func (s *fakeStore) Ticket() store.TicketStore                { return nil }
func (s *fakeStore) TicketNote() store.TicketNoteStore        { return nil }
func (s *fakeStore) Job() store.JobStore                      { return nil }
func (s *fakeStore) CronJob() store.CronJobStore              { return nil }
func (s *fakeStore) Messages() store.MessageStore             { return nil }
func (s *fakeStore) ChatMessages() store.ChatMessageStore     { return nil }
func (s *fakeStore) PeerAgents() store.PeerAgentStore         { return s.peers }
func (s *fakeStore) PeerBindClaims() store.PeerBindClaimStore { return nil }
func (s *fakeStore) Users() store.UserStore                   { return nil }
func (s *fakeStore) OncallGroups() store.OncallGroupStore {
	return nil
}
func (s *fakeStore) OncallSchedules() store.OncallScheduleStore    { return nil }
func (s *fakeStore) TicketEscalation() store.TicketEscalationStore { return nil }
func (s *fakeStore) Metadata() store.MetadataStore                 { return nil }

func TestProcessChatMemberBotAdded(t *testing.T) {
	const chatID = "oc_test_group"

	session := &fakeSessionStore{}
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: session}}}

	event := &larkim.P2ChatMemberBotAddedV1{
		Event: &larkim.P2ChatMemberBotAddedV1Data{ChatId: ptr.To(chatID)},
	}

	if err := b.ProcessChatMemberBotAdded(context.Background(), event); err != nil {
		t.Fatalf("ProcessChatMemberBotAdded returned error: %v", err)
	}

	if session.created == nil {
		t.Fatal("ProcessChatMemberBotAdded did not create a session")
	}
	if got := session.created.SessionID; got != chatID {
		t.Errorf("SessionID = %q, want %q", got, chatID)
	}
	if got := session.created.ChatID; got != chatID {
		t.Errorf("ChatID = %q, want %q", got, chatID)
	}
	if got := session.created.SessionType; got != model.SessionTypeNormalGroup {
		t.Errorf("SessionType = %q, want %q", got, model.SessionTypeNormalGroup)
	}
}

func TestProcessChatMemberBotAddedNilEvent(t *testing.T) {
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: &fakeSessionStore{}}}}

	if err := b.ProcessChatMemberBotAdded(context.Background(), nil); err != nil {
		t.Fatalf("ProcessChatMemberBotAdded(nil) returned error: %v", err)
	}
}

func TestProcessChatMemberBotDeleted(t *testing.T) {
	const chatID = "oc_test_group"

	session := &fakeSessionStore{}
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: session}}}

	event := &larkim.P2ChatMemberBotDeletedV1{
		Event: &larkim.P2ChatMemberBotDeletedV1Data{ChatId: ptr.To(chatID)},
	}

	if err := b.ProcessChatMemberBotDeleted(context.Background(), event); err != nil {
		t.Fatalf("ProcessChatMemberBotDeleted returned error: %v", err)
	}

	if session.deleted == nil {
		t.Fatal("ProcessChatMemberBotDeleted did not delete a session")
	}
	if got := session.deleted.Filters["session_id"]; got != chatID {
		t.Errorf("delete filter session_id = %v, want %q", got, chatID)
	}
}

func TestProcessChatMemberBotDeletedNilEvent(t *testing.T) {
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: &fakeSessionStore{}}}}

	if err := b.ProcessChatMemberBotDeleted(context.Background(), nil); err != nil {
		t.Fatalf("ProcessChatMemberBotDeleted(nil) returned error: %v", err)
	}
}

func TestProcessChatDisbanded(t *testing.T) {
	const chatID = "oc_test_group"

	session := &fakeSessionStore{}
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: session}}}

	event := &larkim.P2ChatDisbandedV1{
		Event: &larkim.P2ChatDisbandedV1Data{ChatId: ptr.To(chatID)},
	}

	if err := b.ProcessChatDisbanded(context.Background(), event); err != nil {
		t.Fatalf("ProcessChatDisbanded returned error: %v", err)
	}

	if session.deleted == nil {
		t.Fatal("ProcessChatDisbanded did not delete a session")
	}
	if got := session.deleted.Filters["session_id"]; got != chatID {
		t.Errorf("delete filter session_id = %v, want %q", got, chatID)
	}
}

func TestProcessChatDisbandedNilEvent(t *testing.T) {
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: &fakeSessionStore{}}}}

	if err := b.ProcessChatDisbanded(context.Background(), nil); err != nil {
		t.Fatalf("ProcessChatDisbanded(nil) returned error: %v", err)
	}
}

func TestProcessP2pChatEntered(t *testing.T) {
	const (
		openID = "ou_test_user"
		userID = "u_test_user"
	)

	session := &fakeSessionStore{}
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: session}}}

	event := &larkim.P2ChatAccessEventBotP2pChatEnteredV1{
		Event: &larkim.P2ChatAccessEventBotP2pChatEnteredV1Data{
			OperatorId: &larkim.UserId{OpenId: ptr.To(openID), UserId: ptr.To(userID)},
		},
	}

	if err := b.ProcessP2pChatEntered(context.Background(), event); err != nil {
		t.Fatalf("ProcessP2pChatEntered returned error: %v", err)
	}

	if session.created == nil {
		t.Fatal("ProcessP2pChatEntered did not create a session")
	}
	// The key has to be the address a message from this user resolves to — their
	// user_id, for one of ours — because a session filed under anything else is a
	// row no lookup ever finds.
	if got := session.created.SessionID; got != userID {
		t.Errorf("SessionID = %q, want %q (the user_id, not the open_id)", got, userID)
	}
	if got := session.created.ChatID; got != userID {
		t.Errorf("ChatID = %q, want %q", got, userID)
	}
	if got := session.created.SessionType; got != model.SessionTypePrivateChat {
		t.Errorf("SessionType = %q, want %q", got, model.SessionTypePrivateChat)
	}
}

func TestProcessP2pChatEnteredExisting(t *testing.T) {
	const userID = "u_test_user"

	session := &fakeSessionStore{
		getFn: func(_ context.Context, _ *where.Options) (*model.SessionM, error) {
			return &model.SessionM{SessionID: userID}, nil
		},
	}
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: session}}}

	event := &larkim.P2ChatAccessEventBotP2pChatEnteredV1{
		Event: &larkim.P2ChatAccessEventBotP2pChatEnteredV1Data{
			OperatorId: &larkim.UserId{OpenId: ptr.To("ou_test_user"), UserId: ptr.To(userID)},
		},
	}

	if err := b.ProcessP2pChatEntered(context.Background(), event); err != nil {
		t.Fatalf("ProcessP2pChatEntered returned error: %v", err)
	}
	if session.created != nil {
		t.Fatal("ProcessP2pChatEntered created a duplicate session for an existing user_id")
	}
}

// TestProcessP2pChatEnteredWithoutIdentity pins the shape of a sender outside
// this tenant: the platform reports an open_id and no user_id. The session is
// still filed — under the address a message from that sender resolves to, their
// open_id — because filing nothing leaves missing the one row the inbound path
// will look up, which is the outcome that makes the conversation invisible.
func TestProcessP2pChatEnteredWithoutIdentity(t *testing.T) {
	const openID = "ou_test_user"

	session := &fakeSessionStore{}
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: session}}}

	event := &larkim.P2ChatAccessEventBotP2pChatEnteredV1{
		Event: &larkim.P2ChatAccessEventBotP2pChatEnteredV1Data{
			OperatorId: &larkim.UserId{OpenId: ptr.To(openID)},
		},
	}

	if err := b.ProcessP2pChatEntered(context.Background(), event); err != nil {
		t.Fatalf("ProcessP2pChatEntered returned error: %v", err)
	}
	if session.created == nil {
		t.Fatal("ProcessP2pChatEntered filed nothing for a sender carrying only an open_id")
	}
	if got := session.created.SessionID; got != openID {
		t.Errorf("SessionID = %q, want %q (the address a message from this sender resolves to)", got, openID)
	}
}

// TestProcessP2pChatEnteredWithNeitherID pins the only case with no key at all:
// the platform reported neither id, so there is nothing to file the session under
// and nothing a later message could collide with.
func TestProcessP2pChatEnteredWithNeitherID(t *testing.T) {
	session := &fakeSessionStore{}
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: session}}}

	event := &larkim.P2ChatAccessEventBotP2pChatEnteredV1{
		Event: &larkim.P2ChatAccessEventBotP2pChatEnteredV1Data{
			OperatorId: &larkim.UserId{},
		},
	}

	if err := b.ProcessP2pChatEntered(context.Background(), event); err != nil {
		t.Fatalf("ProcessP2pChatEntered returned error: %v", err)
	}
	if session.created != nil {
		t.Fatal("ProcessP2pChatEntered filed a session with no id to address it by")
	}
}

func TestProcessP2pChatEnteredNilEvent(t *testing.T) {
	b := &botBiz{sr: &runtime.StaticRuntime{Store: &fakeStore{session: &fakeSessionStore{}}}}

	if err := b.ProcessP2pChatEntered(context.Background(), nil); err != nil {
		t.Fatalf("ProcessP2pChatEntered(nil) returned error: %v", err)
	}
}

// TestResolveCardReceiver pins which conversation a card click is routed to.
// Getting this wrong is invisible from the card: the click arrives, the callback
// resolves to a conversation that is not the one the card lives in, and every
// lookup against it — the run to stop, the message to answer — finds nothing.
func TestResolveCardReceiver(t *testing.T) {
	const (
		groupChatID   = "oc_group_chat"
		clicker       = "ou_clicker"
		clickerUserID = "u_clicker"
		otherUser     = "ou_peer"
		messageID     = "om_card"
	)

	tests := []struct {
		name     string
		value    map[string]interface{}
		wantType string
		wantID   string
	}{
		{
			// The case that was broken: in a group the conversation is the chat,
			// not the person who happened to click.
			name:     "group card names its chat",
			value:    map[string]interface{}{"session_key": groupChatID, "chat_type": "group"},
			wantType: v1.ReceiverTypeChatID,
			wantID:   groupChatID,
		},
		{
			// Cards rendered before chat_type was written still resolve, because a
			// key that is not the clicker's own id can only be a group's chat id.
			name:     "group card with only a session key",
			value:    map[string]interface{}{"session_key": groupChatID},
			wantType: v1.ReceiverTypeChatID,
			wantID:   groupChatID,
		},
		{
			// A direct message is keyed by the person the bot is talking to, which
			// is the clicker for a click on the bot's own card. The conversation is
			// named by the key the card carries, so the clicker's open_id and their
			// user_id are two different conversations — the reply has to go to the
			// one the card belongs to.
			name:     "direct message card keyed by the clicker's user_id",
			value:    map[string]interface{}{"session_key": clickerUserID},
			wantType: v1.ReceiverTypeUserID,
			wantID:   clickerUserID,
		},
		{
			// The same case on a card rendered before identities moved to user_id:
			// the key is an open_id, and it has to be sent back as one.
			name:     "legacy direct message card keyed by the clicker's open_id",
			value:    map[string]interface{}{"session_key": clicker},
			wantType: v1.ReceiverTypeOpenID,
			wantID:   clicker,
		},
		{
			name:     "direct message card states its type",
			value:    map[string]interface{}{"session_key": otherUser, "chat_type": "p2p"},
			wantType: v1.ReceiverTypeOpenID,
			wantID:   otherUser,
		},
		{
			// A group card whose value is empty has only the platform's report to
			// go on, and the chat id is what it says.
			name:     "group card carries nothing but chat_type",
			value:    map[string]interface{}{"chat_type": "group"},
			wantType: v1.ReceiverTypeChatID,
			wantID:   groupChatID,
		},
		{
			// The oldest fallback, kept for a value that says nothing at all: the
			// clicker, as a direct message — addressed by identity, not by open_id.
			name:     "empty value falls back to the clicker",
			value:    map[string]interface{}{},
			wantType: v1.ReceiverTypeUserID,
			wantID:   clickerUserID,
		},
		{
			name:     "nil value falls back to the clicker",
			value:    nil,
			wantType: v1.ReceiverTypeUserID,
			wantID:   clickerUserID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recv := resolveCardReceiver(tt.value, v1.NewSender(clickerUserID, clicker), groupChatID, messageID)

			if recv.Type != tt.wantType {
				t.Errorf("Type = %q, want %q (it is a receive_id_type, not a chat type)",
					recv.Type, tt.wantType)
			}
			if recv.Id != tt.wantID {
				t.Errorf("Id = %q, want %q", recv.Id, tt.wantID)
			}
			if recv.MessageId != messageID {
				t.Errorf("MessageId = %q, want %q", recv.MessageId, messageID)
			}
			// The target carries no identity: it answers where, and the clicker's
			// identity travels beside it as a Sender — now a compile-time fact, since
			// Receiver has no field that could hold one. That the identity never
			// falls back to the open_id is pinned in TestSenderIdentityNeverFallsBack,
			// and that a card press acts as the clicker by the card-nav tests.
			// Type and Id have to agree: Id is sent as the receive_id of that type.
			switch recv.Type {
			case v1.ReceiverTypeChatID:
				if !recv.IsGroup() {
					t.Errorf("Type %q does not read as a group chat", recv.Type)
				}
			case v1.ReceiverTypeUserID, v1.ReceiverTypeOpenID:
				if !recv.IsP2P() {
					t.Errorf("Type %q does not read as a direct message", recv.Type)
				}
			}
		})
	}
}

// TestResolveCardReceiverFromRenderedCard is the boundary test: it takes the
// callback value out of a card this package actually renders and resolves it,
// rather than checking the two halves separately. A value that passes both of
// those checks can still be wired up wrong — the button writes one key and the
// resolver reads another — and that failure looks exactly like a dead button.
func TestResolveCardReceiverFromRenderedCard(t *testing.T) {
	const (
		runID       = "run-abc-123"
		groupChatID = "oc_group_chat"
		clicker     = "ou_clicker"
	)

	payload, err := buildStreamingCardJSON("", "text", false,
		stopButton{RunID: runID, SessionKey: groupChatID, ChatType: v1.ChatTypeGroup}, cardkitPrint{})
	if err != nil {
		t.Fatalf("buildStreamingCardJSON returned error: %v", err)
	}
	button := stopButtonOf(t, stopRowOf(t, payload))

	for where, value := range callbackValues(button) {
		recv := resolveCardReceiver(value, v1.NewSender("u_clicker", clicker), groupChatID, "om_card")
		if recv.Id != groupChatID {
			t.Errorf("the %s value resolved to conversation %q, want %q — a group click must "+
				"not be routed to the person who clicked", where, recv.Id, groupChatID)
		}
		if !recv.IsGroup() {
			t.Errorf("the %s value resolved to Type %q, want %q", where, recv.Type, v1.ReceiverTypeChatID)
		}
		if runID, _ := value["run_id"].(string); runID == "" {
			t.Errorf("the %s value carries no run id", where)
		}
	}
}
