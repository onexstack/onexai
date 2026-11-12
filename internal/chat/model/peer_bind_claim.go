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

package model

import (
	"time"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&PeerBindClaimM{}) }

// TableNamePeerBindClaimM returns the physical table name under the configured
// dialect: chat.peer_bind_claim on PostgreSQL, onexai_chat_peer_bind_claim on
// MySQL.
func TableNamePeerBindClaimM() string {
	return dbschema.Table(dbschema.Chat, "peer_bind_claim")
}

// PeerBindClaimM is one row of chat.peer_bind_claim: the chat side's half of a
// binding handshake.
//
// # What it is for
//
// A binding needs two facts that live on different sides. The chat side knows
// which chat identity spoke; the business side knows which of its accounts is
// logged in. The one-time code the user carries between them is what joins the
// two, and this row is where the chat side parks its half until the business
// side comes to read it.
//
// # It carries no userID and no agent names, on purpose
//
// The chat service never learns a business userID — it only has to say "this
// code was spoken by this chat identity". Keeping the claim to that one fact is
// what lets the same table serve any business system.
//
// # It exists only because the dependency runs one way
//
// If the chat service could call into the business side, the bot would post the
// code and the identity straight to it and this table would not be needed. It
// cannot: the business side polls here instead.
type PeerBindClaimM struct {
	// Code is the one-time code the user carried in, and the primary key. It is
	// issued by the business side, so nothing here validates its shape.
	Code string `gorm:"column:code;primaryKey;size:64" json:"code"`

	// Backend and Scope mean what they mean on PeerAgentM: the class of caller
	// and the scope inside which PeerID is unique. All three together name one
	// peer, so a claim and the binding it produces use the same triple.
	Backend string `gorm:"column:backend;size:32" json:"backend"`
	Scope   string `gorm:"column:scope;size:64" json:"scope"`

	// PeerID is the chat identity that spoke (Lark: ou_xxx).
	PeerID string `gorm:"column:peer_id;size:128" json:"peerID"`

	// ConfirmHash is the SHA-256 of the confirmation code the bot spoke back in
	// the same conversation (see newConfirmCode), as lowercase hex.
	//
	// # Why the claim carries a second secret at all
	//
	// Speaking the code used to be the whole of it: whoever said it first was the
	// identity the business side went on to bind, and the business side had no way
	// to ask "and is that you?". A code pasted where others could read it was a
	// code the room could take.
	//
	// The fix is not to make the code harder to leak — it is already 32 random
	// bytes and there is no stopping a screenshot. It is that the business side
	// now hands the half it cannot supply itself back to the conversation: this
	// digest is over a string only the speaker was told, so completing the binding
	// needs the account's console session *and* having been the one who spoke.
	// Either half alone finishes nothing.
	//
	// It is `json:"-"` because the one reader that needs it is the internal
	// handler (internal/chat/handler/peer.go), which names the field explicitly.
	//
	// Empty means "no confirmation code was issued", which is what rows written
	// before this column existed carry. The business side fails closed on those
	// rather than treating an empty digest as a match.
	ConfirmHash string `gorm:"column:confirm_hash;size:64" json:"-"`

	// ClaimedAt is when the chat side heard the code, and ExpiresAt is when this
	// row stops being an answer.
	ClaimedAt time.Time `gorm:"column:claimed_at" json:"claimedAt"`
	ExpiresAt time.Time `gorm:"column:expires_at;index" json:"expiresAt"`
}

// TableName returns the physical table name.
func (*PeerBindClaimM) TableName() string { return TableNamePeerBindClaimM() }
