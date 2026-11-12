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
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/im"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/metrics"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/store"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

// BuildCommandContext builds the per-request DynamicRuntime for a card-action
// event: the receiver, the mentions and (lazily) the session/ticket metadata.
// Message events arrive as already-parsed IM events instead — see
// buildInboundRuntime and funnel.go.
func (b *botBiz) BuildCommandContext(ctx context.Context, event any) (*runtime.DynamicRuntime, error) {
	cardEvent, ok := event.(*callback.CardActionTriggerEvent)
	if !ok {
		return nil, fmt.Errorf("unsupported event type")
	}
	return b.runtimeFor(ctx, b.cardEventContext(cardEvent))
}

// buildInboundRuntime builds the per-request DynamicRuntime for a message that
// arrived as an IM event.
func (b *botBiz) buildInboundRuntime(ctx context.Context, ev *im.Event, marks *inboundMarks) (*runtime.DynamicRuntime, error) {
	message := ev.Message
	mentions := make([]runtime.MentionedUser, 0, len(message.Mentions))
	for _, m := range message.Mentions {
		mentions = append(mentions, runtime.MentionedUser{
			UserID: m.UserID,
			OpenID: m.OpenID,
			Name:   m.Name,
			Key:    m.Key,
			IsBot:  m.IsBot,
		})
	}
	// The sender is materialized once and then used for both answers — where a
	// reply goes and who is asking — so the two can never be derived from
	// different readings of the same event.
	sender := v1.NewSender(message.SenderID, message.SenderOpenID)
	return b.runtimeFor(ctx, &eventContext{
		sender:   sender,
		recv:     receiverForIMEvent(sender, message),
		mentions: mentions,
		marks:    marks,
	})
}

// runtimeFor attaches the session/ticket metadata to a resolved event context.
func (b *botBiz) runtimeFor(ctx context.Context, ec *eventContext) (*runtime.DynamicRuntime, error) {
	recv := ec.recv

	var (
		sessionM *model.SessionM
		ticketM  *model.TicketM
		err      error
	)

	if recv.Id != "" {
		if sessionM, err = b.sr.Store.Session().Get(ctx, where.F("session_id", recv.Id)); err != nil {
			if !errors.Is(err, store.ErrRecordNotFound) {
				return nil, err
			}
		}
	}

	if sessionM != nil && sessionM.AssociatedTicketID != "" {
		if ticketM, err = b.sr.Store.Ticket().Get(ctx, where.F("ticket_id", sessionM.AssociatedTicketID)); err != nil {
			if !errors.Is(err, store.ErrRecordNotFound) {
				return nil, err
			}
		}
	}
	// The two reads are marked separately because they fail differently: the
	// session read is a store lookup (and, on a pool that has gone quiet, a
	// reconnect), while the role reads are an in-process walk over the RBAC
	// listers. Telling "the database was slow" from "we recomputed the role set"
	// needs them apart.
	ec.marks.markSession()
	isSRE := b.sr.IsSREUser(ec.sender.Identity())
	isAdmin := b.sr.IsAdminUser(ec.sender.Identity())
	ec.marks.markRoles()

	dr := &runtime.DynamicRuntime{
		Source:       runtime.RequestSourceLark,
		IsSRE:        isSRE,
		IsAdmin:      isAdmin,
		Sender:       ec.sender,
		Receiver:     recv,
		ConvID:       recv.Id,
		IsCardAction: ec.isCardAction,
		SessionM:     sessionM,
		TicketM:      ticketM,
		Mentions:     ec.mentions,
		LarkServices: b,
		// The command package cannot reach the scheduler, and which agents this
		// caller runs as is only visible through one: see DynamicRuntime's field.
		ResolvedAgents: b.resolvedAgentsOnce(),
	}
	ec.marks.markRuntime()
	return dr, nil
}

// eventContext carries the fields resolved from an inbound Lark event, before
// session/ticket metadata is attached.
//
// The caller travels as one Sender, which is what makes the two ids the platform
// reports for them impossible to confuse: the receiver built alongside answers
// where a reply goes, and the Sender answers who is asking. Both are derived from
// it, and nothing here reads a platform field twice.
type eventContext struct {
	sender       v1.Sender
	recv         *v1.Receiver
	mentions     []runtime.MentionedUser
	isCardAction bool
	// marks, when set, receives the timestamps of the legs this context goes
	// through. It is nil everywhere except the message path — a card action or a
	// menu command is not a message and prints no latency line.
	marks *inboundMarks
}

// senderOfCardOperator is the card path's one reading of who acted — the mirror
// of v1.NewSender(msg.SenderID, msg.SenderOpenID) on the message path. Several
// handlers answer a card callback and all of them act as the same person, so the
// mapping from the event to a Sender lives here rather than in each of them.
//
// # The operator's open_id is read here too
//
// A card action carries the operator's open_id, and it used to be parsed and
// then dropped — the button press was treated as coming from somebody with no
// app-scoped identity. That is only invisible while nothing consults it: the
// peer binding is looked up by open_id, so anything that resolves a declaration
// on this path would answer "you have no binding" for a user who has one.
func senderOfCardOperator(event *callback.CardActionTriggerEvent) v1.Sender {
	if event == nil || event.Event == nil || event.Event.Operator == nil {
		return v1.NewSender("", "")
	}
	return v1.NewSender(
		ptr.FromOr(event.Event.Operator.UserID, ""),
		event.Event.Operator.OpenID,
	)
}

// cardEventContext resolves the context for a card-action event.
func (b *botBiz) cardEventContext(event *callback.CardActionTriggerEvent) *eventContext {
	return &eventContext{
		sender:       senderOfCardOperator(event),
		recv:         b.parseReceiverFromCardEvent(event),
		isCardAction: true,
	}
}

// receiverForIMEvent resolves where to reply to a message event: the sender's
// private conversation for a P2P message, the group chat otherwise.
//
// The sender arrives already materialized — the caller builds one Sender per
// event and hands the same value here — so the question of which id a reply is
// addressed to is answered in exactly one place, for every entry point. A
// tenant-mate's user_id serves as both identity and address, which is what made
// the two look like one question; a sender outside this tenant has no user_id at
// all, and it is the open_id that keeps their reply from being sent to nobody.
func receiverForIMEvent(sender v1.Sender, message *im.Message) *v1.Receiver {
	if message.ChatType == im.ConvTypeP2P {
		return v1.DirectReceiver(sender, message.ID, message.ParentID)
	}
	return v1.ChatReceiver(message.ConvID, message.ID, message.ParentID)
}

// ProcessMessage runs an inbound message down the ladder in funnel.go.
func (b *botBiz) ProcessMessage(ctx context.Context, event *im.Event) error {
	if event == nil {
		return nil
	}
	return b.handleInbound(ctx, newInbound(event, time.Now()))
}

// handleCommandMessage executes a "/command" message via the cobra command tree.
func (b *botBiz) handleCommandMessage(ctx context.Context, dr *runtime.DynamicRuntime, text string, timings *commandTimings) error {
	cmdText := strings.TrimSpace(strings.TrimPrefix(text, "/"))
	if cmdText == "" {
		return nil
	}

	args, err := b.parseMessageToArgs(cmdText)
	if err != nil {
		return nil
	}

	// The latency line is written whatever becomes of the command, including a
	// refusal: "the bot was slow to say no" is the same question.
	commandName := ""
	if len(args) > 0 {
		commandName = args[0]
	}
	defer timings.finish(ctx, commandName, messageIDOf(dr))

	exec := func() error {
		responder := runtime.NewLarkResponder(dr)
		cmd := command.NewBotCommandTree(ctx, b.sr, responder)
		timings.markTree()
		command.HideCommands(cmd, dr)
		cmd.SetOut(responder)
		cmd.SetErr(responder)
		cmd.SetArgs(args)
		cmd.SetContext(ctx)

		// The flush is where the reply is actually sent, and it runs in a defer
		// so a failure has no error return to travel out on. It is reported
		// here rather than discarded: a reply that failed to send was
		// indistinguishable, from every seat except this one, from a reply the
		// bot never attempted — and the first symptom was a user saying the bot
		// had ignored them.
		defer func() {
			flusher, ok := responder.(interface{ Flush(context.Context) error })
			if !ok {
				return
			}
			if err := flusher.Flush(ctx); err != nil {
				metrics.M.RecordOutboundFailure(ctx, "lark")
				slog.ErrorContext(ctx, "command output was not delivered",
					"cmd", strings.Join(args, " "), "error", err)
			}
			timings.markSent()
		}()

		err := cmd.ExecuteContext(ctx)
		timings.markExecuted()
		if err != nil {
			if !runtime.IsUserError(err) {
				// args can be empty: a message of only quote characters parses
				// to no tokens, and indexing it here would panic the handler on
				// input any group member can send.
				name := ""
				if len(args) > 0 {
					name = args[0]
				}
				metrics.M.RecordLarkCommandError(ctx, name)
			}
			return err
		}
		return nil
	}

	// Card commands update the card in place; an OnIt/DONE reaction on the
	// triggering message is meaningless there, so it is message-only.
	if dr.IsCardAction {
		return exec()
	}
	return b.withProcessingReaction(ctx, dr.Receiver, exec)
}

// ProcessMessageRecalled records a message-recall event so the recalled message
// is not processed (and is evicted from any in-flight handling). The event
// carries only the message and chat ID, not the message body.
func (b *botBiz) ProcessMessageRecalled(ctx context.Context, event *larkim.P2MessageRecalledV1) error {
	if event == nil || event.Event == nil || event.Event.MessageId == nil {
		return nil
	}
	b.markMessageRecalled(*event.Event.MessageId)
	slog.InfoContext(ctx, "marked message recalled", "message_id", *event.Event.MessageId)
	return nil
}

// ProcessChatMemberBotAdded persists a group session when the bot is added to
// a group chat. The session is keyed by the group chat_id so subsequent group
// interactions resolve to a stable session context.
func (b *botBiz) ProcessChatMemberBotAdded(ctx context.Context, event *larkim.P2ChatMemberBotAddedV1) error {
	if event == nil || event.Event == nil || event.Event.ChatId == nil {
		return nil
	}

	chatID := *event.Event.ChatId
	sessionM := &model.SessionM{
		SessionID:   chatID,
		ChatID:      chatID,
		SessionType: model.SessionTypeNormalGroup,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := b.sr.Store.Session().Create(ctx, sessionM); err != nil {
		slog.ErrorContext(ctx, "failed to persist session",
			"chat_id", chatID,
			"session_type", model.SessionTypeNormalGroup,
			"error", err,
		)
		return fmt.Errorf("failed to persist session: %w", err)
	}
	slog.InfoContext(ctx, "created group session",
		"chat_id", chatID,
		"session_type", model.SessionTypeNormalGroup,
	)
	return nil
}

// ProcessChatMemberBotDeleted removes the group session when the bot is removed
// from a group chat. Deletion is idempotent: a missing session is not an error.
func (b *botBiz) ProcessChatMemberBotDeleted(ctx context.Context, event *larkim.P2ChatMemberBotDeletedV1) error {
	if event == nil || event.Event == nil || event.Event.ChatId == nil {
		return nil
	}
	return b.deleteGroupSession(ctx, *event.Event.ChatId)
}

// ProcessChatDisbanded removes the group session when a group is disbanded.
// Deletion is idempotent: a missing session is not an error.
func (b *botBiz) ProcessChatDisbanded(ctx context.Context, event *larkim.P2ChatDisbandedV1) error {
	if event == nil || event.Event == nil || event.Event.ChatId == nil {
		return nil
	}
	return b.deleteGroupSession(ctx, *event.Event.ChatId)
}

// deleteGroupSession deletes the session keyed by the given group chat_id.
func (b *botBiz) deleteGroupSession(ctx context.Context, chatID string) error {
	if err := b.sr.Store.Session().Delete(ctx, where.F("session_id", chatID)); err != nil {
		slog.ErrorContext(ctx, "failed to delete session", "chat_id", chatID, "error", err)
		return fmt.Errorf("failed to delete session: %w", err)
	}
	slog.InfoContext(ctx, "deleted group session", "chat_id", chatID)
	return nil
}

// ProcessP2pChatEntered upserts a private-chat session when a user opens a P2P
// conversation with the bot. The session is keyed by the address a message from
// that user resolves to, which is what makes the row findable: the inbound path
// keys it by the sender's user_id, and by their open_id when they are outside this
// tenant — keying it by anything else produced a row no lookup could ever find.
// Re-entry is idempotent: an existing session is reused, not duplicated.
func (b *botBiz) ProcessP2pChatEntered(ctx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
	if event == nil || event.Event == nil || event.Event.OperatorId == nil {
		return nil
	}
	sender := v1.NewSender(
		ptr.From(event.Event.OperatorId.UserId),
		ptr.From(event.Event.OperatorId.OpenId),
	)
	key := sender.Address()
	if key == "" {
		// The platform reported neither id, so there is no key to file the session
		// under; the inbound path reports the missing identity when it sees one.
		return nil
	}

	if _, err := b.sr.Store.Session().Get(ctx, where.F("session_id", key)); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrRecordNotFound) {
		return err
	}

	sessionM := &model.SessionM{
		SessionID:   key,
		ChatID:      key,
		SessionType: model.SessionTypePrivateChat,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := b.sr.Store.Session().Create(ctx, sessionM); err != nil {
		slog.ErrorContext(ctx, "failed to persist session",
			"session_id", key,
			"session_type", model.SessionTypePrivateChat,
			"error", err,
		)
		return fmt.Errorf("failed to persist session: %w", err)
	}
	slog.InfoContext(ctx, "created private chat session",
		"session_id", key,
		"session_type", model.SessionTypePrivateChat,
	)
	return nil
}

// parseReceiverFromCardEvent resolves where a card callback came from: it pulls
// the fields out of the event and hands them to resolveCardReceiver, which holds
// the decision itself so it can be tested without a Lark event.
func (b *botBiz) parseReceiverFromCardEvent(event *callback.CardActionTriggerEvent) *v1.Receiver {
	if event == nil || event.Event == nil {
		return &v1.Receiver{}
	}
	req := event.Event

	var (
		value                     map[string]interface{}
		openChatID, openMessageID string
	)
	if req.Action != nil {
		value = req.Action.Value
	}
	if req.Context != nil {
		openChatID = req.Context.OpenChatID
		openMessageID = req.Context.OpenMessageID
	}
	operator := senderOfCardOperator(event)

	return resolveCardReceiver(value, operator, openChatID, openMessageID)
}

// resolveCardReceiver decides which conversation a card callback belongs to.
//
// The card is the only thing that knows. Lark reports who clicked and which chat
// the card sits in, but a direct message's conversation is keyed by the person
// the bot is talking to — and for a click on the bot's own card that is the
// clicker, so the platform's report is enough there. For a group it is not: the
// conversation is keyed by the chat id, while the report leads with the
// clicker's open id. A group click resolved that way finds a conversation that
// has no run, which is why a stop can look like it did nothing.
//
// Hence the precedence, most trustworthy first:
//
//  1. session_key — the conversation's own key, carried in the callback value
//     because the card was rendered with it. Correct for both chat kinds.
//  2. chat_type — decides the receive_id_type, and with it whether the chat id
//     or the clicker identifies the conversation.
//  3. An inferred chat_type when the card predates the field being written:
//     a key equal to the clicker's own id can only be a direct message, since
//     a group's key is a chat id and a chat id is never a user id. The clicker's
//     ids are compared in both flavours — cards rendered before the identity
//     moved to user_id carry the open_id as their session_key.
//  4. Nothing at all — the clicker, as a direct message. This is the behaviour
//     cards rendered before any of this was carried get, and it is only right
//     for a direct message.
func resolveCardReceiver(value map[string]interface{}, operator v1.Sender, openChatID, openMessageID string) *v1.Receiver {
	// The clicker arrives as one Sender, so where a direct reply to them goes is
	// decided by the same fallback every other entry point uses. Neither id the
	// clicker carries is written by hand here: a second place that decides who the
	// caller is, is how the message path and the card path came to disagree about
	// a sender who has no user_id in this tenant.
	recv := v1.DirectReceiver(operator, openMessageID, "")

	sessionKey, _ := value["session_key"].(string)
	chatType, _ := value["chat_type"].(string)
	if sessionKey != "" {
		recv.Id = sessionKey
		if chatType == "" && sessionKey != operator.Identity() && sessionKey != operator.Handle() {
			chatType = chat.ChatTypeGroup
		}
	}

	switch chatType {
	case chat.ChatTypeGroup:
		recv.Type = v1.ReceiverTypeChatID
		if sessionKey == "" {
			recv.Id = openChatID
		}
	default:
		// A direct message: stated as p2p, or implied by a key that is not a chat
		// id. The type follows the id rather than the era — a card rendered before
		// the identity moved to user_id carries an open_id as its session key, and
		// sending that value in the user_id namespace is what Lark rejects.
		if sessionKey == "" {
			recv.Id = operator.Address()
		}
		recv.Type = v1.DirectReceiverType(recv.Id)
	}

	return recv
}
