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

package v1

import "strings"

// Receiver type constants.
const (
	// ReceiverTypeOpenID identifies a direct message to a user.
	ReceiverTypeOpenID = "open_id"
	// ReceiverTypeChatID identifies a message to a group chat.
	ReceiverTypeChatID = "chat_id"
	// ReceiverTypeUserID identifies a message to a user by user_id.
	ReceiverTypeUserID = "user_id"
)

// Sender is the caller behind an event, in both namespaces a platform can report
// them in — and it is a type rather than two loose strings because the two answer
// different questions:
//
//   - Identity is the tenant-scoped user_id ("go.kong"). Authorization, record
//     ownership, allow-lists and cross-service subjects all name it; it answers
//     "is this one of ours". It is empty for a sender outside this tenant,
//     because Lark scopes a user_id to its tenant — and it is deliberately never
//     filled from the open_id, since a second identity key is the thing the
//     user_id migration removed.
//   - OpenID is the app-scoped handle. It is what a peer binding is looked up
//     by, and it is what a reply to somebody outside this tenant is addressed
//     to, because they have no user_id here to be addressed by.
//
// The fallback from one to the other exists in exactly one place, Address(). It
// is about delivery and never about identity: addressing a reply to a sender
// with no user_id by their (empty) user_id is how a cross-tenant message ended
// up with nowhere to go ("receiver has no id") while the log said only that the
// identity was unresolved.
type Sender struct {
	userID string
	openID string
}

// NewSender is the one place platform ids become an identity. Keeping the fields
// unexported is what makes that true: there is no struct literal that could
// build a Sender with the open_id in the identity's place.
func NewSender(userID, openID string) Sender {
	return Sender{userID: userID, openID: openID}
}

// Identity is who the caller is — empty when they are not one of ours, which is
// an answer rather than a prompt to reach for another id.
func (s Sender) Identity() string { return s.userID }

// Address is where a direct reply to this caller is sent: their user_id when
// there is one, their app-scoped open_id otherwise. The only delivery fallback
// in the service.
func (s Sender) Address() string {
	if s.userID != "" {
		return s.userID
	}
	return s.openID
}

// Handle is the platform's own handle for the caller, and is empty for any
// surface that carries none. Unlike the other two it is not a policy question:
// it is what the platform said, kept because resources bound by handle (peer
// bindings) are looked up by it.
func (s Sender) Handle() string { return s.openID }

// DirectReceiver and ChatReceiver are the two ways a delivery target is built:
// one addressed to a person, one to a conversation. They sit together on purpose
// — the asymmetry is the platform's, and a pair is harder to miss than a rule,
// which is why there is no third way to name where a reply goes.
//
// Neither carries an identity: a Receiver answers "where", the Sender answers
// "who", and the two travelling together is what made them look interchangeable.
// Receiver.sender_id is left unset for that reason — the identity that used to
// fill it lives on Sender now, and the field stays only because dropping it from
// the proto would regenerate the whole API package for a field nobody reads.
func DirectReceiver(s Sender, messageID, parentID string) *Receiver {
	addr := s.Address()
	return &Receiver{
		Type:            DirectReceiverType(addr),
		Id:              addr,
		MessageId:       messageID,
		ParentMessageId: parentID,
	}
}

// ChatReceiver addresses a conversation rather than a person. Its address never
// falls back to anything: a group is reached by its chat id.
func ChatReceiver(chatID, messageID, parentID string) *Receiver {
	return &Receiver{
		Type:            ReceiverTypeChatID,
		Id:              chatID,
		MessageId:       messageID,
		ParentMessageId: parentID,
	}
}

// DirectReceiverType returns the receive_id_type a direct message addressed to
// the given id must use.
//
// Lark encodes an id's namespace in its prefix, so the type can be read off the
// id itself. That matters when the id came from somewhere other than this
// build: a card rendered before identities moved to user_id carries an open_id,
// and sending that value in the user_id namespace is rejected outright
// ("not a valid {user_id}").
func DirectReceiverType(id string) string {
	switch {
	case strings.HasPrefix(id, "ou_"), strings.HasPrefix(id, "on_"):
		return ReceiverTypeOpenID
	default:
		return ReceiverTypeUserID
	}
}

// Chat type constants.
const (
	// ChatTypeP2P identifies a direct (peer-to-peer) chat.
	ChatTypeP2P = "p2p"
	// ChatTypeGroup identifies a group chat.
	ChatTypeGroup = "group"
)

// ChatType returns "p2p" for direct messages (open_id/user_id) and "group" for
// group chats (chat_id).
func (recv *Receiver) ChatType() string {
	if recv.IsP2P() {
		return ChatTypeP2P
	}
	return ChatTypeGroup
}

// IsP2P reports whether the receiver is a direct message.
func (recv *Receiver) IsP2P() bool {
	return recv.GetType() == ReceiverTypeOpenID || recv.GetType() == ReceiverTypeUserID
}

// IsGroup reports whether the receiver is a group chat.
func (recv *Receiver) IsGroup() bool {
	return recv.GetType() == ReceiverTypeChatID
}
