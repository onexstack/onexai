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

// Package bot implements the Lark bot business logic: it routes inbound Lark
// messages to either a /command or the @bot AI pipeline, manages card callbacks,
// and owns the self-contained ticket workflow.
package bot

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/wire"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkapp "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/im"
	imlark "github.com/onexstack/onexai/internal/chat/im/lark"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/pkg/session"
	"github.com/onexstack/onexai/internal/chat/store"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// ProviderSet defines Wire DI providers for the bot biz layer.
var ProviderSet = wire.NewSet(New, wire.Bind(new(BotBiz), new(*botBiz)))

// BotBiz defines the bot business logic interface.
type BotBiz interface {
	// ProcessMessage runs one inbound message through the ladder in funnel.go.
	// The event is the already-parsed IM event (handler.buildIMEvent), so the
	// bot does not re-parse the raw platform payload.
	ProcessMessage(ctx context.Context, event *im.Event) error
	ProcessMessageRecalled(ctx context.Context, event *larkim.P2MessageRecalledV1) error
	ProcessCard(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error)
	ProcessChatMemberBotAdded(ctx context.Context, event *larkim.P2ChatMemberBotAddedV1) error
	ProcessChatMemberBotDeleted(ctx context.Context, event *larkim.P2ChatMemberBotDeletedV1) error
	ProcessChatDisbanded(ctx context.Context, event *larkim.P2ChatDisbandedV1) error
	ProcessP2pChatEntered(ctx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error
	ProcessBotMenu(ctx context.Context, event *larkapp.P2BotMenuV6) error
	SendText(ctx context.Context, recv *v1.Receiver, content string) error
	ExecuteAPICommand(ctx context.Context, convID, cmdText, userID string, isSRE bool) *runtime.CommandResult
	CommandTree(ctx context.Context) []*v1.CommandNode
}

var _ BotBiz = (*botBiz)(nil)

// botBiz directly implements runtime.LarkServices: every outbound Lark action
// exposed to commands and the responder is a method on botBiz itself, so no
// adapter indirection is needed.
var _ runtime.LarkServices = (*botBiz)(nil)

// botBiz handles inbound Lark messages: /commands, @bot AI and card callbacks.
type botBiz struct {
	sr      *runtime.StaticRuntime
	retrier *imlark.Retrier

	// cardNav, when set, renders the next card for "nav:"/"act:" card actions.
	cardNav CardNavHandler

	// cardMsgMu guards cardMsgIDs, the last card message ID tracked per session
	// key so a navigation/action can refresh the card in place.
	cardMsgMu  sync.Mutex
	cardMsgIDs map[string]string

	// recalledMu guards recalled, a bounded (TTL-evicted) set of message IDs
	// that were recalled by their sender and must no longer be processed.
	recalledMu sync.Mutex
	recalled   map[string]time.Time

	// claimsMu guards claims, the messages that have already produced a turn,
	// keyed by conversation and message ID. It is what makes "one message, one
	// turn, one card" true regardless of how many times the platform delivers the
	// message; see claimTrigger in admission.go.
	claimsMu sync.Mutex
	claims   map[string]time.Time
	// claimsSweptAt is when claims was last swept of expired entries, so that the
	// sweep runs on a timer rather than on every message.
	claimsSweptAt time.Time

	// imageBatchMu guards imageBatches, the pending image batches keyed by
	// session (receiver id). Consecutive image messages are coalesced within
	// ImageBatchWindow before being flushed.
	imageBatchMu sync.Mutex
	imageBatches map[string]*imageBatch

	// suspendedMu guards suspended, the turns parked on a human decision, keyed by
	// session. A turn that pauses is waiting on somebody rather than on the
	// backend, so nothing inside the run will ever end it: a stop clicked on its
	// card has to find the turn here to close it. Entries are bounded by the
	// number of live conversations and are removed when the turn ends.
	suspendedMu sync.Mutex
	suspended   map[string]*aiTurn

	// expiredMu guards expiredPauses: the run each conversation's last pause was
	// waiting on when its wait ran out.
	//
	// It is a record of what this process has already given up on, and it exists
	// because the turn itself does not outlive the expiry: the wait running out
	// closes the card and deregisters the turn, so by the time a button that was
	// still on somebody's screen is clicked there is nothing left to ask. Without
	// this the click would be delivered to a backend that has forgotten the run and
	// come back as "the delivery failed, please try again" — which is an invitation
	// to retry something that can never work, when the pause is in fact over.
	//
	// One entry per conversation, replaced by the next pause and bounded by the
	// number of conversations, the same way the parked turns are.
	expiredMu     sync.Mutex
	expiredPauses map[string]string
}

// recalledTTL is how long a recalled message ID is remembered.
const recalledTTL = 10 * time.Minute

// New creates a botBiz instance.
//
// Only the per-process caches are created here. The conversation's own state —
// which backend it runs on, and the preferences the user set for it — lives in
// the session manager, not on this struct: the bot is rebuilt when the process
// starts, and anything kept here is forgotten with it.
func New(sr *runtime.StaticRuntime) *botBiz {
	b := &botBiz{
		sr:           sr,
		retrier:      imlark.NewRetrier(sr.Clientset.L().C(), sr.AppID, sr.AppSecret),
		cardMsgIDs:   make(map[string]string),
		recalled:     make(map[string]time.Time),
		imageBatches: make(map[string]*imageBatch),
		suspended:    make(map[string]*aiTurn),
		claims:       make(map[string]time.Time),
	}
	b.cardNav = b.defaultCardNavHandler
	// Registered back onto the runtime rather than injected through Wire: the
	// bot depends on the runtime, so the runtime cannot depend on the bot, and
	// a provider that took both would be a cycle. The command package reaches
	// its executor through this field and never learns where it came from —
	// the same shape cardNav above uses.
	sr.SkillRunner = b
	sr.CustomRunner = b
	return b
}

// setSuspendedTurn records the turn a conversation is parked on.
func (b *botBiz) setSuspendedTurn(sessionKey string, t *aiTurn) {
	if sessionKey == "" || t == nil {
		return
	}
	b.suspendedMu.Lock()
	defer b.suspendedMu.Unlock()
	// Built on demand rather than only in New: this is a cache of parked turns,
	// and a bot assembled without that constructor would otherwise panic on the
	// first pause instead of recording one.
	if b.suspended == nil {
		b.suspended = make(map[string]*aiTurn)
	}
	b.suspended[sessionKey] = t
}

// suspendedTurn returns the turn this conversation is parked on, or nil. A
// conversation that is not paused has nothing to return, which is the answer that
// lets a stop click tell a parked turn from a running one.
func (b *botBiz) suspendedTurn(sessionKey string) *aiTurn {
	b.suspendedMu.Lock()
	defer b.suspendedMu.Unlock()
	return b.suspended[sessionKey]
}

// clearSuspendedTurn forgets the parked turn, if that is the one recorded. A turn
// that ends after a newer one was parked must not erase the newer one's entry.
func (b *botBiz) clearSuspendedTurn(sessionKey string, t *aiTurn) {
	b.suspendedMu.Lock()
	defer b.suspendedMu.Unlock()
	if b.suspended[sessionKey] == t {
		delete(b.suspended, sessionKey)
	}
}

// recordExpiredPause notes that a conversation's pause ran out of time, so that a
// click on a button that has since been taken off the card is refused as expired
// rather than reported as a delivery that failed.
func (b *botBiz) recordExpiredPause(sessionKey, runID string) {
	if sessionKey == "" || runID == "" {
		return
	}
	b.expiredMu.Lock()
	defer b.expiredMu.Unlock()
	if b.expiredPauses == nil {
		b.expiredPauses = make(map[string]string)
	}
	b.expiredPauses[sessionKey] = runID
}

// pauseExpired reports whether sessionKey's last pause ran out of time while
// waiting on runID.
func (b *botBiz) pauseExpired(sessionKey, runID string) bool {
	if sessionKey == "" || runID == "" {
		return false
	}
	b.expiredMu.Lock()
	defer b.expiredMu.Unlock()
	return b.expiredPauses[sessionKey] == runID
}

// clearExpiredPause forgets the expired pause, because the conversation has a new
// one: whatever a late click names now, it is not the pause this process last
// gave up on, and the newest pause is the truth about what the conversation is
// waiting for.
func (b *botBiz) clearExpiredPause(sessionKey string) {
	if sessionKey == "" {
		return
	}
	b.expiredMu.Lock()
	defer b.expiredMu.Unlock()
	delete(b.expiredPauses, sessionKey)
}

// SetCardNavHandler installs the card navigation handler used to render the
// next card for "nav:"/"act:" actions.
func (b *botBiz) SetCardNavHandler(h CardNavHandler) {
	b.cardNav = h
}

// reasoningEnabled reports whether reasoning output is shown for this
// conversation.
//
// The default is on — the behaviour this service had before the preference
// could be turned off — and it is what a conversation that has never been
// toggled, or one the manager does not hold, reports. A preference that could
// not be read must not silently change what the user sees.
func (b *botBiz) reasoningEnabled(sessionKey string) bool {
	return b.conversationState(sessionKey).Mode != session.ModeReasoningOff
}

// setReasoning records the conversation's reasoning toggle.
//
// A snapshot that cannot be written is logged rather than returned: the toggle
// has already taken effect in memory, so the card the user sees is correct, and
// the next write tries again.
func (b *botBiz) setReasoning(sessionKey string, on bool) {
	if err := b.updateConversationState(sessionKey, func(st *session.State) {
		st.Mode = session.ModeReasoningOn
		if !on {
			st.Mode = session.ModeReasoningOff
		}
	}); err != nil {
		slog.Warn("failed to record the reasoning preference",
			"conv_id", sessionKey, "reasoning", on, "error", err)
	}
}

// sessionRefs returns what a conversation currently declares it wants, or nil
// when it declares nothing (which means "follow the session type's template").
//
// It reads the session row rather than a per-process snapshot: the declaration
// has to mean the same thing to every instance answering this conversation, and
// a disk-backed copy is one instance's opinion.
func (b *botBiz) sessionRefs(ctx context.Context, sessionID string) []string {
	if b.sr.Store == nil || sessionID == "" {
		return nil
	}
	m, err := b.sr.Store.Session().Get(ctx, where.F("session_id", sessionID))
	if err != nil || m == nil {
		return nil
	}
	return m.RefList()
}

// setSessionRefs replaces a conversation's declaration.
//
// An empty list clears it, and clearing is not the same as setting `["@default"]`:
// clearing defers to the session type's template and keeps deferring when that
// template changes, while naming the default freezes today's answer into the row.
func (b *botBiz) setSessionRefs(ctx context.Context, sessionID string, refs []string) error {
	if b.sr.Store == nil || sessionID == "" {
		return nil
	}
	return b.sr.Store.Session().SetRefs(ctx, sessionID, model.EncodeNameList(refs))
}

// conversationState reads the conversation's stored state, or the zero State
// when there is none.
//
// It looks the conversation up without creating it: a read must not bring a
// conversation into existence, or a card render would leave a snapshot behind
// for every conversation anyone ever looked at.
func (b *botBiz) conversationState(sessionKey string) session.State {
	if b.sr.Sessions == nil || sessionKey == "" {
		return session.State{}
	}
	sess, ok := b.sr.Sessions.Lookup(sessionKey)
	if !ok {
		return session.State{}
	}
	return sess.State()
}

// updateConversationState applies fn to the conversation's stored state,
// creating the conversation if it does not exist yet — a preference is set
// deliberately, so the conversation it belongs to has to exist to hold it.
func (b *botBiz) updateConversationState(sessionKey string, fn func(*session.State)) error {
	if b.sr.Sessions == nil || sessionKey == "" {
		return nil
	}
	sess, err := b.sr.Sessions.Session(sessionKey)
	if err != nil {
		return err
	}
	return sess.Update(fn)
}

// markMessageRecalled records a message ID as recalled.
func (b *botBiz) markMessageRecalled(msgID string) {
	if msgID == "" {
		return
	}
	b.recalledMu.Lock()
	b.recalled[msgID] = time.Now()
	b.recalledMu.Unlock()
}

// isMessageRecalled reports whether the message was recalled within the TTL,
// evicting stale entries on the way.
func (b *botBiz) isMessageRecalled(msgID string) bool {
	if msgID == "" {
		return false
	}
	b.recalledMu.Lock()
	defer b.recalledMu.Unlock()
	t, ok := b.recalled[msgID]
	if !ok {
		return false
	}
	if time.Since(t) > recalledTTL {
		delete(b.recalled, msgID)
		return false
	}
	return true
}

// sessionFor returns the session row for a conversation, or nil when there is
// none.
//
// A miss is reported as an absent row rather than as an error to be handled at
// every call site: a conversation that has no row yet is an ordinary state, and
// a read that cannot find one must not bring it into existence — the same rule
// conversationState follows for the same reason.
func (b *botBiz) sessionFor(ctx context.Context, convID string) *model.SessionM {
	if b.sr.Store == nil || convID == "" {
		return nil
	}

	sessionM, err := b.sr.Store.Session().Get(ctx, where.F("session_id", convID))
	if err != nil {
		if !errors.Is(err, store.ErrRecordNotFound) {
			slog.WarnContext(ctx, "cannot read a conversation's session row", "conv_id", convID, "error", err)
		}
		return nil
	}
	return sessionM
}
