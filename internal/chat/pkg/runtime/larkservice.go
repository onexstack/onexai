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

package runtime

import (
	"context"

	"github.com/onexstack/onexai/internal/chat/pkg/stream"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// MentionedUser carries the identity of a user @-mentioned in a message.
type MentionedUser struct {
	UserID string
	OpenID string
	Name   string
	// Key is the placeholder the platform left in the message text for this
	// mention, e.g. "@_user_1". It is the only stable link between a mention and
	// its position in the body: the display name may be empty, and a body that
	// still carries the placeholder is indistinguishable from one that does not
	// without it.
	Key string
	// IsBot is true when the mention targets a bot (a mention without a user_id).
	IsBot bool
}

// RichTextElement is a single node in a Lark rich-text (post) message.
type RichTextElement struct {
	Tag      string `json:"tag"`
	Text     string `json:"text,omitempty"`
	Href     string `json:"href,omitempty"`
	UserId   string `json:"user_id,omitempty"`
	ImageKey string `json:"image_key,omitempty"`
}

// RichTextContent is a rich-text (post) message body: a title plus a matrix of
// lines, each a list of elements.
type RichTextContent struct {
	Title   string              `json:"title,omitempty"`
	Content [][]RichTextElement `json:"content"`
}

// LarkServices abstracts all Lark-specific outbound actions so commands (and the
// biz layer) never import the Lark SDK directly. It is implemented by the bot
// biz layer for Lark callers and nil for REST/MCP callers.
type LarkServices interface {
	SendText(ctx context.Context, recv *v1.Receiver, content string) error
	SendCard(ctx context.Context, recv *v1.Receiver, templateID string, variables map[string]interface{}) error
	SendMarkdownCard(ctx context.Context, recv *v1.Receiver, title, mdContent, color string) error
	SendTextAndGetID(ctx context.Context, recv *v1.Receiver, content string) (string, error)
	PatchCard(ctx context.Context, messageID, templateID string, variables map[string]interface{}) error

	CreateChat(ctx context.Context, senderID, title string, primaryOncaller *v1.Watcher) (string, error)
	InviteMembers(ctx context.Context, chatID, memberIDType string, members []string) error

	// ResolveUserName returns a Lark user's display name, or "" when it cannot
	// be resolved (unknown user, transport trouble, or an app without the
	// contact scope). The ticket path records who raised a ticket with it, so
	// an empty answer leaves the name unset rather than failing the command.
	ResolveUserName(ctx context.Context, userID string) string

	// UpdateChatName renames a group chat. The duty commands use it to keep a
	// ticket group's name in step with its status and level
	// ("[处理中][P3]title" — in progress, priority 3). It is a no-op for non-Lark callers, which have no
	// LarkServices at all.
	UpdateChatName(ctx context.Context, chatID, name string) error
	// UpdateChatAvatar replaces a group chat's avatar. imageKey is a Lark
	// image_key; an empty key must be skipped by the caller, not sent.
	UpdateChatAvatar(ctx context.Context, chatID, imageKey string) error

	CreateStreamingCard(ctx context.Context, recv *v1.Receiver, title, initialText string, showReasoning bool, stopRunID string) (string, error)
	// The incremental card updates take a *stream.Sequence rather than an int on
	// purpose. The platform rejects an update whose sequence does not exceed the
	// previous one, silently, and three lanes plus a final close all writing to
	// one card is exactly the situation where hand-written arithmetic gets it
	// wrong. Taking the counter itself makes `seq + 1` a compile error rather
	// than a card that stops updating for no visible reason.
	UpdateCardText(ctx context.Context, cardID, text string, seq *stream.Sequence) error
	UpdateCardReasoningText(ctx context.Context, cardID, text string, seq *stream.Sequence) error
	UpdateCardProgressText(ctx context.Context, cardID, text string, seq *stream.Sequence) error

	// The component-level writes. A card is built before anything is known about
	// whether the run will pause, and a pause has to put its buttons on the card
	// the reader is already looking at — which means adding components to a card
	// that is on screen, and taking them away again when they have been answered.
	//
	// They are element writes rather than whole-card updates on purpose: a card
	// rewritten wholesale loses the reader's collapsed panels, and the decision
	// block is meant to be read while the answer streams into the card above it.
	UpdateCardElementText(ctx context.Context, cardID, elementID, text string, seq *stream.Sequence) error
	InsertCardElements(ctx context.Context, cardID, target string, seq *stream.Sequence, elements ...map[string]any) error
	DeleteCardElements(ctx context.Context, cardID string, seq *stream.Sequence, ids ...string) error
	// SetCardSummary rewrites the summary the conversation list shows for a card.
	// It is how a card parked on a decision says so where a reader who has not
	// opened it can see.
	SetCardSummary(ctx context.Context, cardID, summary string, seq *stream.Sequence) error
	// CloseStreamingCard finalizes a streaming card, replacing it with a
	// prebuilt final card JSON (typically larkcard.BuildRichCard).
	CloseStreamingCard(ctx context.Context, cardID, cardJSON string, seq *stream.Sequence) error

	GetRecentMessages(ctx context.Context, recv *v1.Receiver, limit int) (string, error)

	SendRichText(ctx context.Context, recv *v1.Receiver, content *RichTextContent) error
	SendInteractiveCard(ctx context.Context, recv *v1.Receiver, cardContent interface{}) error

	SendImage(ctx context.Context, recv *v1.Receiver, data []byte) error
	SendFile(ctx context.Context, recv *v1.Receiver, fileName, mimeType string, data []byte) error

	// AddReaction attaches an emoji reaction to a message and returns the
	// reaction id (needed to remove it later). emojiType is a Lark reaction type
	// such as "OnIt" or "DONE".
	AddReaction(ctx context.Context, messageID, emojiType string) (string, error)
	// RemoveReaction removes a previously-added reaction from a message.
	RemoveReaction(ctx context.Context, messageID, reactionID string) error
}
