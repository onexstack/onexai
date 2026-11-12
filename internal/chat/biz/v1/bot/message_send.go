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
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	imlark "github.com/onexstack/onexai/internal/chat/im/lark"
	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// lark returns the underlying Lark SDK client, or nil when no IM backend
// is configured.
func (b *botBiz) lark() *lark.Client {
	return b.sr.Clientset.L().C()
}

// ResolveUserName returns a Lark user's display name, or "" when it cannot be
// resolved (no client, unknown user, an app without the contact scope). The
// ticket path uses it to record who raised a ticket; an empty answer is
// carried through as an unset name rather than failing the command.
func (b *botBiz) ResolveUserName(ctx context.Context, userID string) string {
	return imlark.ResolveUserName(ctx, b.lark(), userID)
}

// SendText sends a plain text message to the given receiver.
func (b *botBiz) SendText(ctx context.Context, recv *v1.Receiver, content string) error {
	_, err := b.SendTextAndGetID(ctx, recv, content)
	return err
}

// SendTextAndGetID sends a plain text message and returns the message ID.
func (b *botBiz) SendTextAndGetID(ctx context.Context, recv *v1.Receiver, content string) (string, error) {
	// Preserve the pre-mention text for the TUI mirror; the mention conversion
	// below would leak <at> tags into the TUI.
	original := content

	// Convert "@name" tokens into Lark <at> mention tags so they render as real
	// @mentions in Feishu.
	content = b.applyOutboundMentions(ctx, recv.Id, content)

	contentBytes, _ := json.Marshal(content)
	jsonStr := fmt.Sprintf(`{"text":%s}`, string(contentBytes))

	msgID, err := b.sendMessage(ctx, recv, larkim.MsgTypeText, jsonStr)
	if err != nil {
		return "", err
	}
	b.mirrorToTUI(recv, original)
	return msgID, nil
}

// sendMessage is the shared template for creating or replying a Lark IM message:
// resolve the client, build the request, call the API with retry, and normalize
// the error. When the receiver carries a triggering message ID, the reply is
// threaded under that message; otherwise a new message is created. It returns
// the resulting message ID.
func (b *botBiz) sendMessage(ctx context.Context, recv *v1.Receiver, msgType, content string) (string, error) {
	if recv == nil || recv.Id == "" {
		return "", fmt.Errorf("send %s message: receiver has no id", msgType)
	}
	if err := b.waitOutbound(ctx); err != nil {
		return "", err
	}
	if b.shouldReplyInThread(recv) {
		return b.replyMessage(ctx, recv, msgType, content)
	}
	return b.createMessage(ctx, recv, msgType, content)
}

// waitOutbound blocks until the outbound rate limiter admits another message,
// or ctx is cancelled. It is a no-op when rate limiting is disabled.
func (b *botBiz) waitOutbound(ctx context.Context) error {
	if b.sr.OutboundLimiter == nil {
		return nil
	}
	return b.sr.OutboundLimiter.Wait(ctx)
}

// shouldReplyInThread reports whether the reply should be threaded under the
// triggering message (i.e. reply_to_trigger is enabled and the receiver carries
// a message ID to reply to).
func (b *botBiz) shouldReplyInThread(recv *v1.Receiver) bool {
	return b.sr.ReplyToTrigger && recv != nil && recv.MessageId != ""
}

// createMessage creates a new message in the receiver's conversation, wrapped
// with tenant-token/transient retry.
func (b *botBiz) createMessage(ctx context.Context, recv *v1.Receiver, msgType, content string) (string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(recv.Type).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(recv.Id).
			MsgType(msgType).
			Content(content).
			Build()).
		Build()

	var msgID string
	err := b.retrier.Do(ctx, "send "+msgType, func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		resp, err := client.Im.Message.Create(ctx, req, options...)
		if err != nil {
			return fmt.Errorf("failed to send %s message: %w", msgType, err)
		}
		if !resp.Success() {
			return fmt.Errorf("send %s message failed, code=%d, msg=%s", msgType, resp.Code, resp.Msg)
		}
		if resp.Data != nil && resp.Data.MessageId != nil {
			msgID = *resp.Data.MessageId
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return msgID, nil
}

// replyMessage replies to the receiver's triggering message, wrapped with
// tenant-token/transient retry.
func (b *botBiz) replyMessage(ctx context.Context, recv *v1.Receiver, msgType, content string) (string, error) {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(recv.MessageId).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(msgType).
			Content(content).
			Build()).
		Build()

	var msgID string
	err := b.retrier.Do(ctx, "reply "+msgType, func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		resp, err := client.Im.Message.Reply(ctx, req, options...)
		if err != nil {
			return fmt.Errorf("failed to reply %s message: %w", msgType, err)
		}
		if !resp.Success() {
			return fmt.Errorf("reply %s message failed, code=%d, msg=%s", msgType, resp.Code, resp.Msg)
		}
		if resp.Data != nil && resp.Data.MessageId != nil {
			msgID = *resp.Data.MessageId
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return msgID, nil
}

// SendCard sends a Lark card template to the given receiver.
func (b *botBiz) SendCard(ctx context.Context, recv *v1.Receiver, templateID string, variables map[string]interface{}) error {
	card := &callback.Card{
		Type: "template",
		Data: &callback.TemplateCard{
			TemplateID:       templateID,
			TemplateVariable: variables,
		},
	}
	return b.SendRawCard(ctx, recv, card)
}

// PatchCard updates an existing card message by message ID.
func (b *botBiz) PatchCard(ctx context.Context, messageID, templateID string, variables map[string]interface{}) error {
	templateCard := &callback.TemplateCard{
		TemplateID:       templateID,
		TemplateVariable: variables,
	}
	cardBytes, err := json.Marshal(map[string]interface{}{
		"type": "template",
		"data": templateCard,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal template card: %w", err)
	}

	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(string(cardBytes)).
			Build()).
		Build()

	resp, err := b.lark().Im.Message.Patch(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to patch card: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("patch card API error: %s", resp.Msg)
	}
	return nil
}

// SendMarkdownCard sends a markdown-formatted card to the given receiver.
//
// A body too large to render as a card goes out as text instead, split across
// as many messages as the per-message budget needs. The card is a rendering
// choice, not a delivery guarantee: a markdown body is one indivisible element,
// so there is no way to compact it down the way a rich card sheds steps, and
// truncating it to fit would drop the tail of the answer — silently, in a
// channel where the user has no way to tell that anything is missing.
func (b *botBiz) SendMarkdownCard(ctx context.Context, recv *v1.Receiver, title, markdown, themeColor string) error {
	cardContent := buildMarkdownCard(title, markdown, themeColor)
	if !fitsAsCard(cardContent) {
		slog.WarnContext(ctx, "markdown card exceeds the Feishu payload limit, sending as text",
			"limit", larkcard.MaxCardJSONBytes, "markdown_bytes", len(markdown))
		return b.sendMarkdownAsText(ctx, recv, title, markdown)
	}
	return b.SendRawCard(ctx, recv, cardContent)
}

// sendMarkdownAsText delivers a markdown body as one or more plain text
// messages, reusing the same splitter the streaming responder uses so both
// outbound surfaces break long content at the same places.
func (b *botBiz) sendMarkdownAsText(ctx context.Context, recv *v1.Receiver, title, markdown string) error {
	if title != "" {
		markdown = title + "\n\n" + markdown
	}
	for _, chunk := range larkcard.SplitByBudget(markdown, larkcard.TextChunkBytes) {
		if err := b.SendText(ctx, recv, chunk); err != nil {
			return fmt.Errorf("send markdown as text: %w", err)
		}
	}
	return nil
}

// fitsAsCard reports whether a card payload is within the size Feishu accepts.
//
// A payload that cannot be marshalled counts as not fitting. The fallback is a
// text send that still delivers the content, so an unexpected marshalling
// failure costs the card and not the answer.
func fitsAsCard(cardContent interface{}) bool {
	b, err := json.Marshal(cardContent)
	return err == nil && len(b) <= larkcard.MaxCardJSONBytes
}

// SendCardModel renders and sends a typed larkcard.Card to the receiver. It is
// the structured counterpart to SendRawCard, letting callers build cards via
// the larkcard builder instead of hand-assembling JSON maps.
func (b *botBiz) SendCardModel(ctx context.Context, recv *v1.Receiver, card *larkcard.Card) error {
	if card == nil {
		return fmt.Errorf("send card model: nil card")
	}
	cardJSON := larkcard.RenderCard(card, recv.Id)
	_, err := b.sendMessage(ctx, recv, larkim.MsgTypeInteractive, cardJSON)
	if err != nil {
		return err
	}
	b.mirrorToTUI(recv, card.RenderText())
	return nil
}

// PatchCardMessage updates an existing card message in place with a new typed
// card. The sessionKey is injected into the rendered card's callback values so
// subsequent clicks route back to the same conversation.
func (b *botBiz) PatchCardMessage(ctx context.Context, messageID, sessionKey string, card *larkcard.Card) error {
	if messageID == "" {
		return fmt.Errorf("patch card message: empty message id")
	}
	if card == nil {
		return fmt.Errorf("patch card message: nil card")
	}
	cardJSON := larkcard.RenderCard(card, sessionKey)

	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(cardJSON).
			Build()).
		Build()

	return b.retrier.Do(ctx, "patch card", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		resp, err := client.Im.Message.Patch(ctx, req, options...)
		if err != nil {
			return fmt.Errorf("patch card: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("patch card failed, code=%d, msg=%s", resp.Code, resp.Msg)
		}
		return nil
	})
}

// SendInteractiveCard sends an arbitrary interactive card JSON to the receiver.
func (b *botBiz) SendInteractiveCard(ctx context.Context, recv *v1.Receiver, cardContent interface{}) error {
	return b.SendRawCard(ctx, recv, cardContent)
}

// SendRawCard sends a raw card payload to the given receiver.
func (b *botBiz) SendRawCard(ctx context.Context, recv *v1.Receiver, cardContent interface{}) error {
	cardJSONBytes, err := json.Marshal(cardContent)
	if err != nil {
		return fmt.Errorf("marshal card content failed: %w", err)
	}

	_, err = b.sendMessage(ctx, recv, larkim.MsgTypeInteractive, string(cardJSONBytes))
	if err != nil {
		return err
	}
	// Mirror the card's readable text into the TUI (template cards with no
	// extractable text yield "" and are skipped).
	b.mirrorToTUI(recv, imlark.ExtractCardText(string(cardJSONBytes)))
	return nil
}

// SendRichText sends a rich-text (post) message.
func (b *botBiz) SendRichText(ctx context.Context, recv *v1.Receiver, content *runtime.RichTextContent) error {
	richTextJSON, err := buildRichTextJSON(content)
	if err != nil {
		return fmt.Errorf("failed to build rich text content: %w", err)
	}

	_, err = b.sendMessage(ctx, recv, larkim.MsgTypePost, richTextJSON)
	if err != nil {
		return err
	}
	b.mirrorToTUI(recv, richTextPlain(content))
	return nil
}

// richTextPlain flattens a RichTextContent into plain display text for the TUI.
// text/link elements contribute their text; @mentions render as "@name" (or the
// user id); images render as a placeholder. Returns "" for empty content.
func richTextPlain(content *runtime.RichTextContent) string {
	if content == nil {
		return ""
	}
	var parts []string
	if content.Title != "" {
		parts = append(parts, content.Title)
	}
	for _, line := range content.Content {
		var seg strings.Builder
		for _, e := range line {
			switch e.Tag {
			case "at":
				name := e.UserId
				if name == "" {
					name = e.Text
				}
				if name != "" {
					seg.WriteString("@" + name)
				}
			case "img":
				seg.WriteString("[图片]")
			default: // text, a
				seg.WriteString(e.Text)
			}
		}
		if s := seg.String(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// buildRichTextJSON serializes a RichTextContent into Lark post JSON.
func buildRichTextJSON(content *runtime.RichTextContent) (string, error) {
	if content == nil || len(content.Content) == 0 {
		return "", fmt.Errorf("content cannot be empty")
	}
	for i, line := range content.Content {
		if len(line) == 0 {
			return "", fmt.Errorf("line %d cannot be empty", i)
		}
		for j, element := range line {
			if err := validateRichTextElement(element); err != nil {
				return "", fmt.Errorf("invalid element at line %d, position %d: %w", i, j, err)
			}
		}
	}
	postData := map[string]interface{}{
		"zh_cn": map[string]interface{}{
			"title":   content.Title,
			"content": content.Content,
		},
	}
	jsonData, err := json.Marshal(postData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal rich text content: %w", err)
	}
	return string(jsonData), nil
}

func validateRichTextElement(element runtime.RichTextElement) error {
	switch element.Tag {
	case "text":
		if element.Text == "" {
			return fmt.Errorf("text element must have non-empty text")
		}
	case "a":
		if element.Text == "" || element.Href == "" {
			return fmt.Errorf("link element must have both text and href")
		}
	case "at":
		if element.UserId == "" {
			return fmt.Errorf("at element must have user_id")
		}
	case "img":
		if element.ImageKey == "" {
			return fmt.Errorf("image element must have image_key")
		}
	default:
		return fmt.Errorf("unsupported element tag: %s", element.Tag)
	}
	return nil
}

// buildMarkdownCard builds a schema 2.0 Lark card with wide mode.
func buildMarkdownCard(title, markdown, themeColor string) map[string]interface{} {
	if themeColor == "" {
		themeColor = "blue"
	}
	// width_mode is the schema 2.0 replacement for the 1.0-only
	// wide_screen_mode; keeping the latter makes Feishu reject the
	// card with 230099 "parse card json err". See larkcard.WidthModeFill.
	cardContent := larkcard.NewCardRoot(
		larkcard.CardWidthConfig(),
		[]map[string]interface{}{{"tag": "markdown", "content": markdown}},
	)
	if title != "" {
		cardContent["header"] = map[string]interface{}{
			"template": themeColor,
			"title":    map[string]interface{}{"content": title, "tag": "plain_text"},
		}
	}
	return cardContent
}
