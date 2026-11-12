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

// Package im defines shared IM types used by the Lark backend and business logic.
package im

import (
	"encoding/json"
	"time"

	"github.com/onexstack/onexai/pkg/media"
)

// ConvType represents the type of an IM conversation.
type ConvType string

const (
	ConvTypeGroup    ConvType = "group"
	ConvTypeP2P      ConvType = "p2p"
	ConvTypePlatform ConvType = "platform" // virtual AI-persona conversation
	ConvTypeRobot    ConvType = "robot"    // virtual AI-agent (bot) conversation
)

// EventType represents the type of an IM event.
type EventType string

const (
	EventTypeMessage    EventType = "message"
	EventTypeMemberJoin EventType = "member_join"
	EventTypeMemberLeft EventType = "member_left"
	EventTypeCardAction EventType = "card_action"
)

// Mention is a structured @-mention parsed from an inbound message. Unlike the
// text-flattened form kept in MessageContent.Text, this preserves the mentioned
// user's identity so the pipeline can detect whether the bot was addressed.
type Mention struct {
	// Key is the in-text placeholder, e.g. "@_user_1".
	Key string `json:"key"`
	// OpenID is the mentioned user's open_id (empty for a bot mention on some
	// platforms).
	OpenID string `json:"open_id,omitempty"`
	// UserID is the mentioned user's tenant-scoped ID, when the platform
	// supplies one (a bot mention usually carries none).
	UserID string `json:"user_id,omitempty"`
	// Name is the mentioned user's display name.
	Name string `json:"name"`
	// IsBot reports whether the mention targets a bot.
	IsBot bool `json:"is_bot,omitempty"`
}

// CardAction is an interactive-card button callback (e.g. the "stop" button on
// a streaming AI card).
type CardAction struct {
	// Action is the logical action name carried in the button value, e.g.
	// "askai_stop" | "askai_approve" | "askai_reject".
	Action string `json:"action"`
	// RunID identifies the AI run the action targets.
	RunID string `json:"run_id,omitempty"`
	// ConvID is the conversation the card lives in.
	ConvID string `json:"conv_id,omitempty"`
	// MessageID is the card message ID.
	MessageID string `json:"message_id,omitempty"`
	// OperatorID is the identity (Lark tenant-scoped user_id) of the user who
	// clicked, never an open_id.
	OperatorID string `json:"operator_id,omitempty"`
	// Values carries the full button value map for actions needing extra data.
	Values map[string]string `json:"values,omitempty"`
}

// MessageContent holds a text message.
type MessageContent struct {
	Type string `json:"type"` // "text" | "rich_text" | "card" | "image"
	Text string `json:"text"`
	Raw  string `json:"raw,omitempty"` // original payload from the IM system
	// Images are the message's images, as references into the media store.
	//
	// They ride beside the text rather than inside Raw because Raw is the
	// platform's own payload — for Lark, a body holding an image_key — and it
	// is what the /v1/images re-fetch path needs. A message can carry both:
	// a rich-text post is words and pictures at once.
	Images []media.Ref `json:"images,omitempty"`
}

// Message is a unified IM message.
type Message struct {
	ID     string `json:"id"`
	ConvID string `json:"conv_id"`
	// SenderID is the sender's identity: for Lark the tenant-scoped user_id
	// (e.g. "go.kong"), never an open_id — it is what an operator can name in a
	// config file, and the same value across every application in the tenant.
	// Authorization, record ownership and session keys are keyed by it, so it
	// must be the value handed to a Lark API that asks for user_id.
	SenderID   string         `json:"sender_id"`
	SenderName string         `json:"sender_name"`
	Content    MessageContent `json:"content"`
	CreateTime time.Time      `json:"create_time"`
	IsBot      bool           `json:"is_bot"`
	Backend    string         `json:"backend,omitempty"`
	RawID      string         `json:"raw_id,omitempty"`

	// ChatType distinguishes a P2P (single) chat from a group chat.
	ChatType ConvType `json:"chat_type,omitempty"`
	// SenderOpenID is the sender's Lark open_id: the platform handle, valid
	// inside the Lark adapter and as a receive_id_type, and deliberately not the
	// identity — an open_id is per-application, so it can neither be written
	// into a config file nor compared across services.
	SenderOpenID string `json:"sender_open_id,omitempty"`
	// ParentID is the ID of the quoted/replied-to message, if any.
	ParentID string `json:"parent_id,omitempty"`
	// Mentions holds the structured @-mentions in this message.
	Mentions []Mention `json:"mentions,omitempty"`
}

// Conversation is a unified IM conversation.
type Conversation struct {
	ID          string   `json:"id"`
	Type        ConvType `json:"type"`
	Name        string   `json:"name"`
	AvatarURL   string   `json:"avatar_url,omitempty"`
	Members     []string `json:"members,omitempty"`
	LastMessage *Message `json:"last_message,omitempty"`
	UnreadCount int      `json:"unread_count,omitempty"`
	Backend     string   `json:"backend,omitempty"`
	RawID       string   `json:"raw_id,omitempty"`
}

// MemberChangeInfo holds information about a member join/leave event.
type MemberChangeInfo struct {
	UserID   string `json:"userID"`
	UserName string `json:"userName"`
}

// Event is a unified IM event.
type Event struct {
	Type       EventType         `json:"type"`
	ConvID     string            `json:"convID"`
	Message    *Message          `json:"message"`
	Member     *MemberChangeInfo `json:"member"`
	CardAction *CardAction       `json:"cardAction"`
}

func (evt *Event) String() string {
	data, _ := json.Marshal(evt)
	return string(data)
}

// SendMessageReq is the request to send a message.
type SendMessageReq struct {
	ConvID  string
	Content MessageContent
	// Type is an optional hint for session type.
	// If empty, defaults to P2P / single-chat behaviour.
	Type ConvType
}

// Member is a participant of a conversation.
type Member struct {
	// UserID is the member's identity as the platform reports it: a Lark
	// tenant-scoped user_id for a person, an app_id ("cli_…") for a bot.
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}
