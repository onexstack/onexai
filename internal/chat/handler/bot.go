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

package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkapp "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/onexstack/onexstack/pkg/core"
	"github.com/onexstack/onexstack/pkg/errorsx"
	"github.com/onexstack/onexstack/pkg/ptr"
	"go.opentelemetry.io/otel"

	"github.com/onexstack/onexai/internal/chat/im"
	imlark "github.com/onexstack/onexai/internal/chat/im/lark"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	chat "github.com/onexstack/onexai/pkg/known/chat"
	"github.com/onexstack/onexai/pkg/wshub"
)

// ServeLarkWebhook is the main Lark webhook handler. It reads and decodes the
// raw event body, verifies the request signature (and decrypts the body when an
// Encrypt Key is configured), deduplicates by event ID, then dispatches to the
// appropriate handler.
func (h *Handler) ServeLarkWebhook(c *gin.Context) {
	ctx, span := otel.Tracer("handler").Start(c.Request.Context(), "Handler.ServeLarkWebhook")
	defer span.End()
	c.Request = c.Request.WithContext(ctx)

	// The limit is applied before the body is read because this endpoint is
	// public and unauthenticated at this point: an unbounded ReadAll is a
	// memory-exhaustion request, not a payload.
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			slog.WarnContext(ctx, "Lark webhook body rejected as too large", "limit", maxWebhookBodyBytes)
			core.WriteResponse(c, nil, errno.ErrPayloadTooLarge)
			return
		}
		slog.ErrorContext(ctx, "Failed to read request body", "error", err)
		core.WriteResponse(c, nil, errorsx.ErrBind.WithMessage("Failed to read request body"))
		return
	}

	// Verify the request authenticity (and decrypt an encrypted body when an
	// Encrypt Key is configured). A request that fails verification must never
	// reach the event dispatcher — anyone can POST to a public webhook URL.
	body, err = h.verifyLarkWebhook(c, body)
	if err != nil {
		slog.WarnContext(ctx, "Lark webhook verification failed", "error", err)
		core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage("invalid lark webhook signature"))
		return
	}

	var evt larkevent.EventV2Body
	if err := json.Unmarshal(body, &evt); err != nil {
		slog.ErrorContext(ctx, "Failed to parse webhook event", "error", err, "body", string(body))
		core.WriteResponse(c, nil, errorsx.ErrBind.WithMessage("Invalid JSON format"))
		return
	}

	// Handle URL verification challenge from Lark.
	if evt.Type == "url_verification" {
		slog.InfoContext(ctx, "URL verification challenge received", "challenge", evt.Challenge)
		c.JSON(http.StatusOK, gin.H{"challenge": evt.Challenge})
		return
	}

	// Deduplicate events using a local cache to prevent Lark retries.
	if h.isDuplicate(ctx, evt.Header.EventID) {
		core.WriteResponse(c, nil, nil)
		return
	}

	resp, err := h.handleEventByType(ctx, &evt, body)
	if err != nil {
		// Always return 200 to prevent Lark from retrying.
		core.WriteResponse(c, nil, nil)
		return
	}

	core.WriteResponse(c, resp, nil)
}

// verifyLarkWebhook authenticates a Lark webhook request and returns the
// plaintext event body (decrypted when an Encrypt Key is configured).
//
// The verification follows the two-mode scheme described by the Lark docs:
//   - Encrypt Key configured: the X-Lark-Signature header must equal
//     SHA256(timestamp + nonce + encryptKey + rawBody), computed over the raw
//     (still-encrypted) body. On success the body is decrypted with AES-256-CBC.
//   - Encrypt Key empty: the plaintext body carries a Verification Token in the
//     "token" field that must match the configured value.
//
// When neither token is configured the request is rejected: a fail-open webhook
// would defeat the entire purpose of the check, and configuration drift should be
// loud rather than silent.
func (h *Handler) verifyLarkWebhook(c *gin.Context, body []byte) ([]byte, error) {
	encryptKey := h.larkOptions.EncryptKey
	verificationToken := h.larkOptions.VerificationToken

	if encryptKey != "" {
		want := c.GetHeader(larkevent.EventSignature)
		if want == "" {
			return nil, fmt.Errorf("missing %s header", larkevent.EventSignature)
		}
		got := larkevent.Signature(
			c.GetHeader(larkevent.EventRequestTimestamp),
			c.GetHeader(larkevent.EventRequestNonce),
			encryptKey,
			string(body),
		)
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return nil, fmt.Errorf("signature mismatch")
		}

		// The body is encrypted only when an Encrypt Key is configured. Detect it
		// by shape and decrypt; a plaintext body passes through unchanged.
		var enc larkevent.EventEncryptMsg
		if err := json.Unmarshal(body, &enc); err == nil && enc.Encrypt != "" {
			plain, err := larkevent.EventDecrypt(enc.Encrypt, encryptKey)
			if err != nil {
				return nil, fmt.Errorf("decrypt event body: %w", err)
			}
			return plain, nil
		}
		return body, nil
	}

	if verificationToken != "" {
		var probe struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			return nil, fmt.Errorf("parse verification token: %w", err)
		}
		if subtle.ConstantTimeCompare([]byte(probe.Token), []byte(verificationToken)) != 1 {
			return nil, fmt.Errorf("verification token mismatch")
		}
		return body, nil
	}

	// Neither token configured — reject instead of silently accepting everything.
	return nil, fmt.Errorf("lark webhook security is not configured (encrypt-key or verification-token required)")
}

// webhookEventHandler handles one class of Lark webhook event: it parses the
// body, records the entry log, dispatches to the business layer, and returns the
// webhook response. Each handler is self-contained so adding a new event type
// only requires registering it in webhookEventHandlers.
type webhookEventHandler func(ctx context.Context, body []byte) (any, error)

func (h *Handler) handleEventByType(ctx context.Context, event *larkevent.EventV2Body, body []byte) (any, error) {
	ctx = context.WithoutCancel(ctx)
	eventType := event.Header.EventType

	handler, ok := h.webhookEventHandlers()[eventType]
	if !ok {
		slog.WarnContext(ctx, "Unhandled event type", "event_type", eventType)
		return nil, nil
	}
	return handler(ctx, body)
}

// webhookEventHandlers maps a Lark event type to its handler.
func (h *Handler) webhookEventHandlers() map[string]webhookEventHandler {
	return map[string]webhookEventHandler{
		"im.message.receive_v1":                        h.handleMessageReceive,
		"im.message.recalled_v1":                       h.handleMessageRecalled,
		"card.action.trigger":                          h.handleCardAction,
		"im.chat.member.bot.added_v1":                  h.handleChatMemberBotAdded,
		"im.chat.member.bot.deleted_v1":                h.handleChatMemberBotDeleted,
		"im.chat.disbanded_v1":                         h.handleChatDisbanded,
		"im.chat.access_event.bot_p2p_chat_entered_v1": h.handleP2pChatEntered,
		"application.bot.menu_v6":                      h.handleBotMenu,
	}
}

// handleMessageReceive parses and dispatches an im.message.receive_v1 event.
func (h *Handler) handleMessageReceive(ctx context.Context, body []byte) (any, error) {
	var message larkim.P2MessageReceiveV1
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, fmt.Errorf("failed to parse message receive event: %w", err)
	}

	slog.InfoContext(ctx, "Processing webhook event", messageLogAttrs(&message)...)

	runtime.GoSafe(ctx, "lark.message.dispatch", func() {
		_ = h.processMessageEvent(ctx, &message)
	})
	return nil, nil
}

// handleMessageRecalled parses and dispatches an im.message.recalled_v1 event.
func (h *Handler) handleMessageRecalled(ctx context.Context, body []byte) (any, error) {
	var event larkim.P2MessageRecalledV1
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("failed to parse message recalled event: %w", err)
	}

	runtime.GoSafe(ctx, "lark.message_recalled.dispatch", func() {
		_ = h.processMessageRecalled(ctx, &event)
	})
	return nil, nil
}

// handleCardAction parses and dispatches a card.action.trigger event. The card
// response (an in-place update or toast) is returned synchronously so Lark can
// render it immediately.
func (h *Handler) handleCardAction(ctx context.Context, body []byte) (any, error) {
	var evt callback.CardActionTriggerEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return nil, fmt.Errorf("failed to parse card action event: %w", err)
	}
	return h.processCardActionEvent(ctx, &evt), nil
}

// handleChatMemberBotAdded parses and dispatches an im.chat.member.bot.added_v1
// event, which fires when the bot is added to a group chat.
func (h *Handler) handleChatMemberBotAdded(ctx context.Context, body []byte) (any, error) {
	var event larkim.P2ChatMemberBotAddedV1
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("failed to parse chat member bot added event: %w", err)
	}

	slog.InfoContext(ctx, "processing webhook event", chatMemberBotAddedLogAttrs(&event)...)

	runtime.GoSafe(ctx, "lark.chat_member_bot_added.dispatch", func() {
		_ = h.processChatMemberBotAdded(ctx, &event)
	})
	return nil, nil
}

// handleChatMemberBotDeleted parses and dispatches an im.chat.member.bot.deleted_v1
// event, which fires when the bot is removed from a group chat.
func (h *Handler) handleChatMemberBotDeleted(ctx context.Context, body []byte) (any, error) {
	var event larkim.P2ChatMemberBotDeletedV1
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("failed to parse chat member bot deleted event: %w", err)
	}

	slog.InfoContext(ctx, "processing webhook event", chatMemberBotDeletedLogAttrs(&event)...)

	runtime.GoSafe(ctx, "lark.chat_member_bot_deleted.dispatch", func() {
		_ = h.processChatMemberBotDeleted(ctx, &event)
	})
	return nil, nil
}

// handleChatDisbanded parses and dispatches an im.chat.disbanded_v1 event, which
// fires when a group chat is disbanded.
func (h *Handler) handleChatDisbanded(ctx context.Context, body []byte) (any, error) {
	var event larkim.P2ChatDisbandedV1
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("failed to parse chat disbanded event: %w", err)
	}

	slog.InfoContext(ctx, "processing webhook event", chatDisbandedLogAttrs(&event)...)

	runtime.GoSafe(ctx, "lark.chat_disbanded.dispatch", func() {
		_ = h.processChatDisbanded(ctx, &event)
	})
	return nil, nil
}

// handleP2pChatEntered parses and dispatches an
// im.chat.access_event.bot_p2p_chat_entered_v1 event, which fires when a user
// opens a P2P conversation with the bot.
func (h *Handler) handleP2pChatEntered(ctx context.Context, body []byte) (any, error) {
	var event larkim.P2ChatAccessEventBotP2pChatEnteredV1
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("failed to parse p2p chat entered event: %w", err)
	}

	slog.InfoContext(ctx, "processing webhook event", p2pChatEnteredLogAttrs(&event)...)

	runtime.GoSafe(ctx, "lark.p2p_chat_entered.dispatch", func() {
		_ = h.processP2pChatEntered(ctx, &event)
	})
	return nil, nil
}

// handleBotMenu parses and dispatches an application.bot.menu_v6 event, which
// fires when a user taps an item in the bot's custom menu.
func (h *Handler) handleBotMenu(ctx context.Context, body []byte) (any, error) {
	var event larkapp.P2BotMenuV6
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("failed to parse bot menu event: %w", err)
	}

	runtime.GoSafe(ctx, "lark.bot_menu.dispatch", func() {
		_ = h.processBotMenu(ctx, &event)
	})
	return nil, nil
}

// StartLarkWebSocket starts the Lark WebSocket client to receive events.
func (h *Handler) StartLarkWebSocket() {
	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil {
				return nil
			}
			if h.isDuplicate(ctx, event.EventV2Base.Header.EventID) {
				return nil
			}
			// Answered asynchronously, exactly as the webhook route does it (see
			// handleMessageReceive). The SDK writes the frame's response only after
			// this function returns, so running the turn here means the platform
			// waits out the whole turn for an acknowledgement that arrives too
			// late, and re-delivers the event: that is how one message became two
			// turns and two cards. The context belongs to the client, which
			// outlives the frame, and the turn detaches its own cancellation.
			runtime.GoSafe(ctx, "lark.message.dispatch", func() {
				_ = h.processMessageEvent(ctx, event)
			})
			return nil
		}).
		OnP2MessageRecalledV1(func(ctx context.Context, event *larkim.P2MessageRecalledV1) error {
			if event != nil && event.EventV2Base != nil && event.EventV2Base.Header != nil &&
				h.isDuplicate(ctx, event.EventV2Base.Header.EventID) {
				return nil
			}
			return h.processMessageRecalled(ctx, event)
		}).
		OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			if event == nil {
				return &callback.CardActionTriggerResponse{}, nil
			}
			// Log every callback that reaches us, before the dedup check, so a
			// swallowed click is distinguishable from one that never arrived.
			duplicate := h.isDuplicate(ctx, cardActionEventID(event))
			slog.InfoContext(ctx, "card action event received", cardActionLogAttrs(event, duplicate)...)
			if duplicate {
				return &callback.CardActionTriggerResponse{}, nil
			}
			return h.processCardActionEvent(ctx, event), nil
		}).
		OnP2ChatMemberBotAddedV1(func(ctx context.Context, event *larkim.P2ChatMemberBotAddedV1) error {
			if event != nil && event.EventV2Base != nil && event.EventV2Base.Header != nil &&
				h.isDuplicate(ctx, event.EventV2Base.Header.EventID) {
				return nil
			}
			return h.processChatMemberBotAdded(ctx, event)
		}).
		OnP2ChatMemberBotDeletedV1(func(ctx context.Context, event *larkim.P2ChatMemberBotDeletedV1) error {
			if event != nil && event.EventV2Base != nil && event.EventV2Base.Header != nil &&
				h.isDuplicate(ctx, event.EventV2Base.Header.EventID) {
				return nil
			}
			return h.processChatMemberBotDeleted(ctx, event)
		}).
		OnP2ChatDisbandedV1(func(ctx context.Context, event *larkim.P2ChatDisbandedV1) error {
			if event != nil && event.EventV2Base != nil && event.EventV2Base.Header != nil &&
				h.isDuplicate(ctx, event.EventV2Base.Header.EventID) {
				return nil
			}
			return h.processChatDisbanded(ctx, event)
		}).
		OnP2ChatAccessEventBotP2pChatEnteredV1(func(ctx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
			if event != nil && event.EventV2Base != nil && event.EventV2Base.Header != nil &&
				h.isDuplicate(ctx, event.EventV2Base.Header.EventID) {
				return nil
			}
			return h.processP2pChatEntered(ctx, event)
		}).
		OnP2BotMenuV6(func(ctx context.Context, event *larkapp.P2BotMenuV6) error {
			if event != nil && event.EventV2Base != nil && event.EventV2Base.Header != nil &&
				h.isDuplicate(ctx, event.EventV2Base.Header.EventID) {
				return nil
			}
			return h.processBotMenu(ctx, event)
		})

	cli := larkws.NewClient(
		h.larkOptions.AppID, h.larkOptions.AppSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithDomain(h.larkOptions.URL),
		// Without a logger the SDK says nothing at all: not that the
		// connection came up, not that it dropped and reconnected, not that a
		// frame arrived that it will not answer. Every one of those has been a
		// live question while chasing a card callback that got no reply.
		larkws.WithLogger(imlark.SDKLogger{}),
		larkws.WithOnReady(func() {
			slog.Info("lark websocket connected", "domain", h.larkOptions.URL)
		}),
		larkws.WithOnError(func(err error) {
			slog.Error("lark websocket error", "error", err)
		}),
		larkws.WithOnReconnecting(func() {
			slog.Warn("lark websocket reconnecting")
		}),
		larkws.WithOnReconnected(func() {
			slog.Info("lark websocket reconnected")
		}),
		larkws.WithOnDisconnected(func() {
			slog.Warn("lark websocket disconnected")
		}),
	)

	slog.Info("Starting Lark WebSocket client...", "domain", h.larkOptions.URL)
	// Start parks in select{} for the life of the connection, so returning at
	// all means a connect or reconnect failed — and always with an error. The
	// report is unconditional because `if err != nil` here is provably always
	// true against the current SDK (staticcheck SA4023).
	err := cli.Start(context.Background())
	slog.Error("Lark websocket client stopped", "error", err)
}

// isDuplicate reports whether the event was already seen, and remembers it for
// 1 hour. The check-then-act is guarded by dupMu so concurrent identical event
// IDs (e.g. webhook + websocket racing) are not both processed.
func (h *Handler) isDuplicate(ctx context.Context, eventID string) bool {
	if eventID == "" {
		return false
	}
	h.dupMu.Lock()
	defer h.dupMu.Unlock()
	if _, exists := h.local.Get(eventID); exists {
		slog.InfoContext(ctx, "Duplicate Lark event ignored", "event_id", eventID)
		return true
	}
	_ = h.local.SetWithTTL(eventID, struct{}{}, 1, time.Hour)
	return false
}

// messageLogAttrs extracts entry-log fields from a message event: who did what
// in which conversation. Slash commands record the command name (audit value);
// plain messages do not record their body.
func messageLogAttrs(m *larkim.P2MessageReceiveV1) []any {
	if m == nil || m.Event == nil || m.Event.Message == nil {
		return nil
	}
	message := m.Event.Message
	msgType := ptr.From(message.MessageType)
	attrs := []any{"chat_id", ptr.From(message.ChatId), "chat_type", ptr.From(message.ChatType), "msg_type", msgType}
	if m.Event.Sender != nil && m.Event.Sender.SenderId != nil {
		// The identity, not the platform handle: an open_id in a log line reads
		// as an opaque ou_… that no operator can match to a person.
		attrs = append(attrs, "sender_user_id", ptr.From(m.Event.Sender.SenderId.UserId))
	}
	if text := strings.TrimSpace(extractTextContent(msgType, ptr.From(message.Content))); strings.HasPrefix(text, "/") {
		command, _, _ := strings.Cut(text, " ")
		attrs = append(attrs, "command", command)
	}
	return attrs
}

// chatMemberBotAddedLogAttrs extracts entry-log fields from a bot-added event:
// which group the bot joined.
func chatMemberBotAddedLogAttrs(event *larkim.P2ChatMemberBotAddedV1) []any {
	if event == nil || event.Event == nil {
		return nil
	}
	return []any{"chat_id", ptr.From(event.Event.ChatId)}
}

// chatMemberBotDeletedLogAttrs extracts entry-log fields from a bot-deleted
// event: which group the bot left.
func chatMemberBotDeletedLogAttrs(event *larkim.P2ChatMemberBotDeletedV1) []any {
	if event == nil || event.Event == nil {
		return nil
	}
	return []any{"chat_id", ptr.From(event.Event.ChatId)}
}

// chatDisbandedLogAttrs extracts entry-log fields from a chat-disbanded event:
// which group was disbanded.
func chatDisbandedLogAttrs(event *larkim.P2ChatDisbandedV1) []any {
	if event == nil || event.Event == nil {
		return nil
	}
	return []any{"chat_id", ptr.From(event.Event.ChatId)}
}

// p2pChatEnteredLogAttrs extracts entry-log fields from a p2p-chat-entered
// event: who opened the conversation with the bot.
func p2pChatEnteredLogAttrs(event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) []any {
	if event == nil || event.Event == nil || event.Event.OperatorId == nil {
		return nil
	}
	return []any{"user_id", ptr.From(event.Event.OperatorId.UserId)}
}

// extractTextContent parses only "text" message bodies; other types return "".
func extractTextContent(msgType, content string) string {
	if msgType != "text" || content == "" {
		return ""
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return ""
	}
	return payload.Text
}

// messageReceiver resolves where to reply for a received message: the private
// chat for p2p messages, the group chat otherwise.
//
// It reads the parsed event rather than the SDK payload: processMessageEvent has
// already built one, and parsing the same event twice is how two shapes of it come
// to exist. A direct message is addressed to the person — by user_id when the
// platform reported one, by open_id otherwise, so a cross-tenant sender's error
// and panic notices still have somewhere to go. Failing both, the chat id the
// message arrived in is always a legal destination; this receiver only carries
// notices, so it is never asked which conversation a run belongs to.
func messageReceiver(ev *im.Event) *v1.Receiver {
	if ev == nil || ev.Message == nil {
		return &v1.Receiver{}
	}
	message := ev.Message
	sender := v1.NewSender(message.SenderID, message.SenderOpenID)
	if message.ChatType == im.ConvTypeP2P && sender.Address() != "" {
		return v1.DirectReceiver(sender, message.ID, message.ParentID)
	}
	return v1.ChatReceiver(message.ConvID, message.ID, message.ParentID)
}

// cardReceiver resolves where to reply for a card action.
//
// It is deliberately coarser than the bot layer's own resolution (see
// resolveCardReceiver in biz/v1/bot): its only job is to deliver a notice that
// the click already went wrong, and for that the chat the card sits in is
// always a legal destination — a direct message has a chat id too. The
// conversation a run can be looked up in is a different question, and only the
// card's own callback data answers it.
func cardReceiver(event *callback.CardActionTriggerEvent) *v1.Receiver {
	if event == nil || event.Event == nil {
		return &v1.Receiver{}
	}
	req := event.Event
	var sender v1.Sender
	if req.Operator != nil {
		sender = v1.NewSender(ptr.From(req.Operator.UserID), req.Operator.OpenID)
	}
	messageID, chatID := "", ""
	if req.Context != nil {
		messageID, chatID = req.Context.OpenMessageID, req.Context.OpenChatID
	}
	chatType := ""
	if req.Action != nil {
		chatType, _ = req.Action.Value["chat_type"].(string)
	}
	// A direct message is addressed to the clicker, by user_id or (outside this
	// tenant) by open_id; anything else to the chat the card sits in. The chat id
	// stands as the fallback rather than being overwritten by a clicker id that may
	// be empty — it used to be, which left a notice with nowhere to go.
	if chatType == chat.ChatTypeP2P && sender.Address() != "" {
		return v1.DirectReceiver(sender, messageID, "")
	}
	return v1.ChatReceiver(chatID, messageID, "")
}

// cardActionEventID returns the Lark event ID of a card callback, or "" when
// the envelope carries no header. It survives a nil EventV2Base because the
// embedded base is a pointer and an event without one is still worth logging.
func cardActionEventID(event *callback.CardActionTriggerEvent) string {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil {
		return ""
	}
	return event.EventV2Base.Header.EventID
}

// cardActionLogAttrs extracts the fields needed to trace one card callback: who
// clicked what, in which conversation, and whether the dedup cache swallowed
// it. Both halves matter — a callback that never reaches the server and one
// that arrives with an empty action value look identical from the card, and
// only the raw value tells them apart. The map is logged as-is so an unexpected
// callback shape is visible rather than summarized away.
func cardActionLogAttrs(event *callback.CardActionTriggerEvent, duplicate bool) []any {
	attrs := []any{"duplicate", duplicate}
	if id := cardActionEventID(event); id != "" {
		attrs = append(attrs, "event_id", id)
	}
	req := event.Event
	if req == nil {
		return attrs
	}
	if req.Operator != nil {
		attrs = append(attrs, "operator_open_id", req.Operator.OpenID,
			"operator_user_id", ptr.FromOr(req.Operator.UserID, ""))
	}
	if req.Context != nil {
		attrs = append(attrs, "open_chat_id", req.Context.OpenChatID,
			"open_message_id", req.Context.OpenMessageID)
	}
	if req.Action != nil {
		attrs = append(attrs, "action_tag", req.Action.Tag,
			"action_name", req.Action.Name,
			"action_value", req.Action.Value)
	}
	return attrs
}

// chatMemberBotAddedReceiver resolves where the bot was added: the group chat.
func chatMemberBotAddedReceiver(event *larkim.P2ChatMemberBotAddedV1) *v1.Receiver {
	if event == nil || event.Event == nil {
		return &v1.Receiver{}
	}
	return &v1.Receiver{Type: v1.ReceiverTypeChatID, Id: ptr.From(event.Event.ChatId)}
}

// notifyPanic tells the user their command died unexpectedly. Without this the
// recovery is silent: the command produces no reply at all, and the natural
// reaction is to send it again and panic again.
func (h *Handler) notifyPanic(ctx context.Context, recv *v1.Receiver) {
	text := "命令执行时发生内部错误，请将该 trace id 反馈给 Helper 负责人"
	if traceID := contextx.TraceID(ctx); traceID != "" {
		text = fmt.Sprintf("[%s]%s", traceID, text)
	}
	if recv.Id != "" {
		if err := h.biz.BotV1().SendText(ctx, recv, text); err != nil {
			slog.WarnContext(ctx, "failed to send panic notification", "error", err)
		}
	}
}

func (h *Handler) processMessageEvent(ctx context.Context, message *larkim.P2MessageReceiveV1) error {
	// Fan out: the TUI message relay observes every inbound message, while the
	// bot brain (ProcessMessage) decides whether to reply. Both consume the same
	// parsed event — the raw Lark payload is decoded once, here, rather than once
	// per consumer.
	ev := buildIMEvent(message)
	if ev == nil {
		return nil
	}
	// The relay is a mirror — it persists the message for the TUI and forwards
	// it to the clients watching this conversation — and it decides nothing
	// about the reply. It used to run first and inline, so every message waited
	// on a member-name lookup and a database write before the bot could even
	// look at it. It runs detached now, on a copy of the event: the relay fills
	// in the sender's display name on that copy, and the bot path must not see
	// it change under it.
	relayCtx := context.WithoutCancel(ctx)
	relay := relayEventCopy(ev)
	runtime.GoSafe(relayCtx, "lark.message.relay", func() {
		h.biz.ChatV1().ProcessIMEvent(relayCtx, relay)
	})
	return h.runEvent(ctx, "lark.message", messageReceiver(ev), func() error {
		return h.biz.BotV1().ProcessMessage(ctx, ev)
	})
}

// relayEventCopy returns the event and its message as copies, so the TUI relay
// writing the sender's display name cannot race the bot path reading the same
// struct.
func relayEventCopy(ev *im.Event) *im.Event {
	if ev == nil {
		return nil
	}
	out := *ev
	if ev.Message != nil {
		message := *ev.Message
		if ev.Message.Mentions != nil {
			message.Mentions = append([]im.Mention(nil), ev.Message.Mentions...)
		}
		out.Message = &message
	}
	return &out
}

func (h *Handler) processMessageRecalled(ctx context.Context, event *larkim.P2MessageRecalledV1) error {
	return h.runEvent(ctx, "lark.message_recalled", &v1.Receiver{}, func() error {
		return h.biz.BotV1().ProcessMessageRecalled(ctx, event)
	})
}

func (h *Handler) processCardActionEvent(ctx context.Context, event *callback.CardActionTriggerEvent) *callback.CardActionTriggerResponse {
	defer func() {
		if r := recover(); r != nil {
			runtime.LogRecoveredPanic(ctx, "lark.card_action", r)
			h.notifyPanic(ctx, cardReceiver(event))
		}
	}()

	started := time.Now()
	resp, err := h.biz.BotV1().ProcessCard(ctx, event)
	// The callback budget is 3s; an over-budget answer is dropped by Lark, which
	// looks exactly like a button that does nothing. Record what was answered and
	// how long it took so the two can be told apart after the fact.
	slog.InfoContext(ctx, "card action dispatched",
		"event_id", cardActionEventID(event),
		"response", cardActionResponseKind(resp),
		"elapsed_ms", time.Since(started).Milliseconds(),
		"error", err,
	)
	if err != nil {
		if serr := h.sendError(ctx, cardReceiver(event), err); serr != nil {
			slog.WarnContext(ctx, "failed to send error reply", "error", serr, "source", "lark.card_action")
		}
	}
	if resp == nil {
		return &callback.CardActionTriggerResponse{}
	}
	return resp
}

// cardActionResponseKind names what a card callback answered with, so the log
// distinguishes "rendered a card", "showed a toast" and "answered nothing".
func cardActionResponseKind(resp *callback.CardActionTriggerResponse) string {
	switch {
	case resp == nil:
		return "none"
	case resp.Card != nil && resp.Toast != nil:
		return "card+toast"
	case resp.Card != nil:
		return "card"
	case resp.Toast != nil:
		return "toast"
	default:
		return "empty"
	}
}

func (h *Handler) processChatMemberBotAdded(ctx context.Context, event *larkim.P2ChatMemberBotAddedV1) error {
	return h.runEvent(ctx, "lark.chat_member_bot_added", chatMemberBotAddedReceiver(event), func() error {
		if err := h.biz.BotV1().ProcessChatMemberBotAdded(ctx, event); err != nil {
			return err
		}
		h.notifyConversationsChanged()
		return nil
	})
}

func (h *Handler) processChatMemberBotDeleted(ctx context.Context, event *larkim.P2ChatMemberBotDeletedV1) error {
	return h.runEvent(ctx, "lark.chat_member_bot_deleted", &v1.Receiver{}, func() error {
		if err := h.biz.BotV1().ProcessChatMemberBotDeleted(ctx, event); err != nil {
			return err
		}
		h.notifyConversationsChanged()
		return nil
	})
}

func (h *Handler) processChatDisbanded(ctx context.Context, event *larkim.P2ChatDisbandedV1) error {
	return h.runEvent(ctx, "lark.chat_disbanded", &v1.Receiver{}, func() error {
		if err := h.biz.BotV1().ProcessChatDisbanded(ctx, event); err != nil {
			return err
		}
		h.notifyConversationsChanged()
		return nil
	})
}

// notifyConversationsChanged broadcasts a frame telling all connected TUI
// clients to re-fetch their conversation list (e.g. after the bot is added to
// or removed from a group, or a group is disbanded).
func (h *Handler) notifyConversationsChanged() {
	h.hub.Broadcast(wshub.ServerFrame{Type: wshub.FrameTypeConversationChanged})
}

func (h *Handler) processP2pChatEntered(ctx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
	return h.runEvent(ctx, "lark.p2p_chat_entered", &v1.Receiver{}, func() error {
		return h.biz.BotV1().ProcessP2pChatEntered(ctx, event)
	})
}

// processBotMenu dispatches a bot menu callback to the bot biz layer.
func (h *Handler) processBotMenu(ctx context.Context, event *larkapp.P2BotMenuV6) error {
	return h.runEvent(ctx, "lark.bot_menu", &v1.Receiver{}, func() error {
		return h.biz.BotV1().ProcessBotMenu(ctx, event)
	})
}

// runEvent executes a Lark event handler with panic recovery and unified error
// handling. It recovers here rather than relying on gin's recovery middleware,
// which cannot see a panic raised inside the Lark event dispatcher.
//
// It runs the handler inline, on the caller's goroutine: the in-process callers
// are the dispatcher (one goroutine per message in websocket mode) and the
// webhook handler (one goroutine per HTTP request). A handler that blocks — an
// AI turn does, for minutes — therefore blocks only its own event.
func (h *Handler) runEvent(ctx context.Context, source string, recv *v1.Receiver, fn func() error) error {
	defer func() {
		if r := recover(); r != nil {
			runtime.LogRecoveredPanic(ctx, source, r)
			h.notifyPanic(ctx, recv)
		}
	}()

	if err := fn(); err != nil {
		if serr := h.sendError(ctx, recv, err); serr != nil {
			slog.WarnContext(ctx, "failed to send error reply", "error", serr, "source", source)
		}
	}
	return nil
}

// errorReplyMessage renders the reply for a command error. A UserError is the
// message the user is meant to read, so it is sent verbatim; anything else is
// internal and gets a [traceID] prefix so it can be reported back. A missing
// trace id yields no prefix at all rather than a bare "[]".
func errorReplyMessage(ctx context.Context, err error) string {
	message := err.Error()
	if runtime.IsUserError(err) {
		return message
	}
	if traceID := contextx.TraceID(ctx); traceID != "" {
		return fmt.Sprintf("[%s]%s", traceID, message)
	}
	return message
}

// sendError replies to the user with a user-facing error message.
func (h *Handler) sendError(ctx context.Context, recv *v1.Receiver, err error) error {
	if recv.Id == "" || err == nil {
		return nil
	}
	return h.biz.BotV1().SendText(ctx, recv, errorReplyMessage(ctx, err))
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		if h.larkOptions == nil {
			return
		}
		if h.larkOptions.EventMode == "websocket" {
			go h.StartLarkWebSocket()
		} else {
			engine.POST("/lark/event", h.ServeLarkWebhook)
		}
	})
}
