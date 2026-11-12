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

import "testing"

// The two ids the platform reports for a person are not interchangeable, and
// these tests pin the direction of every derived value so a later reader cannot
// conclude otherwise from the shapes: a user_id is a tenant employee number and
// an open_id carries the ou_ prefix, so only one of them can be sent back as the
// receive_id the platform asked for.
const (
	testUserID = "go.kong"
	testOpenID = "ou_684c62db025820b3af793e43a8b549ea"
)

// TestSenderAddressFallsBackToOpenID pins the single delivery fallback in the
// service. A sender outside this tenant has no user_id — Lark scopes a user_id to
// its tenant — and addressing their reply by that empty value is how a message
// ends up with nowhere to go.
func TestSenderAddressFallsBackToOpenID(t *testing.T) {
	tests := []struct {
		name      string
		sender    Sender
		wantAddr  string
		wantType  string
		wantError bool
	}{
		{
			name:     "one of ours is addressed by their user_id",
			sender:   NewSender(testUserID, testOpenID),
			wantAddr: testUserID,
			wantType: ReceiverTypeUserID,
		},
		{
			name:     "a sender outside this tenant is addressed by their open_id",
			sender:   NewSender("", testOpenID),
			wantAddr: testOpenID,
			wantType: ReceiverTypeOpenID,
		},
		{
			// Nothing to address a reply to. This is the case the sender must be
			// unable to reach by accident: the address type must not be mistaken
			// for an id, and the empty id must stay empty.
			name:     "a sender with neither id has no address",
			sender:   NewSender("", ""),
			wantAddr: "",
			wantType: ReceiverTypeUserID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sender.Address(); got != tt.wantAddr {
				t.Errorf("Address() = %q, want %q", got, tt.wantAddr)
			}

			recv := DirectReceiver(tt.sender, "om_reply", "om_parent")
			if recv.Id != tt.wantAddr {
				t.Errorf("DirectReceiver().Id = %q, want %q", recv.Id, tt.wantAddr)
			}
			if recv.Type != tt.wantType {
				t.Errorf("DirectReceiver().Type = %q, want %q (the id is sent as the receive_id of this type)",
					recv.Type, tt.wantType)
			}
			if recv.MessageId != "om_reply" || recv.ParentMessageId != "om_parent" {
				t.Errorf("DirectReceiver() dropped the thread: MessageId = %q, ParentMessageId = %q",
					recv.MessageId, recv.ParentMessageId)
			}
		})
	}
}

// TestSenderIdentityNeverFallsBack pins the opposite direction, which is the one
// that matters for authorization: a sender with no user_id has no identity, and
// the open_id is not a substitute. Filling it in is how one person comes to hold
// two identities, and how a record ends up owned by an id no configuration can
// name.
func TestSenderIdentityNeverFallsBack(t *testing.T) {
	outsider := NewSender("", testOpenID)

	if got := outsider.Identity(); got != "" {
		t.Errorf("Identity() = %q, want empty: an open_id is an address, not an identity", got)
	}
	if got := outsider.Handle(); got != testOpenID {
		t.Errorf("Handle() = %q, want %q (the platform's own handle is still reported)", got, testOpenID)
	}

	ours := NewSender(testUserID, testOpenID)
	if got := ours.Identity(); got != testUserID {
		t.Errorf("Identity() = %q, want %q", got, testUserID)
	}
}

// TestChatReceiverAddressesTheConversation pins that a group reply is addressed
// by the chat and never by the sender: the two would coincide only if a chat id
// and a user id could be the same string, which is exactly the confusion this
// pair of constructors exists to remove.
func TestChatReceiverAddressesTheConversation(t *testing.T) {
	const chatID = "oc_dcd3fd006eec9a40d559ac9162f1af40"

	recv := ChatReceiver(chatID, "om_reply", "")

	if recv.Type != ReceiverTypeChatID || recv.Id != chatID {
		t.Errorf("ChatReceiver() = {%q, %q}, want {%q, %q}", recv.Type, recv.Id, ReceiverTypeChatID, chatID)
	}
	// The receiver answers where and only where. That it cannot answer "who" is
	// now a compile-time fact: Receiver has no identity field to read, so a chat
	// id cannot be mistaken for a person. See the reserved field in lark.proto.
}
