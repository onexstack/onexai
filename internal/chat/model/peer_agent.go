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

func init() { registry.Register(&PeerAgentM{}) }

// TableNamePeerAgentM returns the physical table name under the configured
// dialect: chat.peer_agent on PostgreSQL, onexai_chat_peer_agent on MySQL.
func TableNamePeerAgentM() string {
	return dbschema.Table(dbschema.Chat, "peer_agent")
}

// PeerAgentM is one row of chat.peer_agent: the agents a chat peer runs, and
// the subject those agents are authorized as.
//
// # PeerID is "the chat platform's name for this person"
//
// The key is whatever the chat backend uses to name a person. The Lark backend
// uses its open_id: external users have no user_id (see the note in
// handler/im_event.go), and an open_id is what the event always carries.
//
// A business system's own identifier (userID) is deliberately absent — it lives
// in that system, not here.
//
// # Backend and Scope each answer half the question, and neither is optional
//
// Backend says which class of caller produced this peer_id: the Lark bot, or
// the OpenAPI surface. Scope says how far that peer_id is unique inside that
// class — for Lark it is the application, because an open_id is app-scoped and
// the same person is a different open_id in a different app.
//
// Both are needed: without backend, a row with an empty scope cannot be read as
// belonging to either class; without scope, a second Lark application would put
// its open_ids into the same key space as the first, and a collision there is a
// silent identity mix-up.
//
// # Canonical is the normalization target
//
// The agent service authorizes by subject, and OneX pushes its rules with the
// subject set to its own userID. A caller that only holds an open_id must
// translate it before calling — this column is what it translates to.
//
// It shares a row with Agents because the two share everything else: written at
// the same instant (a successful binding), deleted together, and keyed by the
// same triple.
//
// # It stores agent names, not "who"
//
// "Which agent" is recorded here; "may this subject use it" is decided by the
// agent service's authz. Keeping them apart is why the rules need not be pushed
// twice for the two call paths.
type PeerAgentM struct {
	// ID is the internal primary key. Nothing addresses a binding by it: a
	// binding is found by the triple or by (canonical), both of which are
	// columns, and no request names one.
	ID uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`

	// Backend is the class of caller that produced PeerID, and carries the same
	// meaning as im.Message.Backend: a bare backend name such as "lark" or
	// "openapi" — never a compound. Putting the application in here would make
	// one name mean two things.
	Backend string `gorm:"column:backend;size:32;uniqueIndex:uq_peer_agent" json:"backend"`

	// Scope is how far PeerID is unique within Backend's class. Lark fills it
	// with the application id; OpenAPI leaves it empty, because caller ids are
	// issued by this platform and are unique already.
	Scope string `gorm:"column:scope;size:64;uniqueIndex:uq_peer_agent" json:"scope"`

	// PeerID is the chat platform's name for this person (Lark: ou_xxx).
	PeerID string `gorm:"column:peer_id;size:128;uniqueIndex:uq_peer_agent" json:"peerID"`

	// Canonical is the subject the agent service authorizes as. No index: the
	// only reader that looks a peer up by a business identity lives on the OneX
	// side, which keeps its own table for that direction. This one only serves
	// lookups by the triple above, so a second index would have no consumer.
	Canonical string `gorm:"column:canonical;size:128" json:"canonical"`

	// Agents is a JSON array of concrete agent names. An empty array is a valid
	// state meaning "bound, but with nothing usable" — it renders differently
	// from "no row at all", which means "not bound".
	Agents string `gorm:"column:agents;type:text" json:"agents"`

	// Source names whoever wrote this row, taken from the credential the writer
	// authenticated with. Its job is audit: with more than one integrator,
	// "which of them pushed this binding, and as what" is the first question
	// asked when something looks wrong.
	Source string `gorm:"column:source;size:64" json:"source"`

	// UpdatedAt is when this binding was last written. A time.Time rather than
	// unix milliseconds because this schema's other tables (chat.session,
	// chat.ticket) are time.Time, and a schema whose timestamps come in two
	// formats is one every reader has to check.
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

// TableName returns the physical table name.
func (*PeerAgentM) TableName() string { return TableNamePeerAgentM() }

// AgentNames decodes the Agents column into a slice. A malformed or empty value
// yields nil rather than an error, the same leniency SessionM.RefList chooses: a
// torn cell renders as "nothing usable" — which every caller already has a reply
// for — rather than failing a turn.
func (m *PeerAgentM) AgentNames() []string { return DecodeNameList(m.Agents) }
