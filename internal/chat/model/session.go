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
	"encoding/json"
	"time"

	"github.com/onexstack/onexstack/pkg/rid"
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/pkg/dbschema"
)

// TableNameSessionM returns the physical table name under the configured
// dialect: chat.session on PostgreSQL, onexai_chat_session on MySQL.
func TableNameSessionM() string { return dbschema.Table(dbschema.Chat, "session") }

// Session type constants are the allowed values for SessionM.SessionType.
const (
	// SessionTypeTicketGroup identifies a session backed by a ticket group chat.
	SessionTypeTicketGroup = "ticket_group"

	// SessionTypeNormalGroup identifies a session backed by a normal group chat.
	SessionTypeNormalGroup = "normal_group"

	// SessionTypePrivateChat identifies a session backed by a private chat.
	SessionTypePrivateChat = "private_chat"

	// SessionTypeOpenAPI identifies a session created via the OpenAPI.
	SessionTypeOpenAPI = "openapi"
)

// SessionM handles session metadata across all interaction channels (Lark
// groups, Lark P2P, OpenAPI, etc.).
type SessionM struct {
	ID                 int64  `gorm:"column:id;primaryKey;autoIncrement:true;comment:主键 ID" json:"id"`
	SessionID          string `gorm:"column:session_id;not null;uniqueIndex;comment:会话 ID" json:"session_id"`               // Group: lark_chat_id; P2P: lark_user_id; OpenAPI: custom
	ChatID             string `gorm:"column:chat_id;not null;comment:聊天/会话展示 ID" json:"chat_id"`                            // generated after creation
	SessionType        string `gorm:"column:session_type;not null;default:'normal_group';comment:会话类型" json:"session_type"` // ticket_group, normal_group, private_chat, openapi
	AssociatedTicketID string `gorm:"column:associated_ticket_id;not null;default:'';comment:关联工单号" json:"associated_ticket_id"`
	// Refs is what this conversation declares it wants, as a JSON array:
	// `["@peer", "ops-agent"]`.
	//
	// It is the **intent**, and the only column the scheduler reads for a
	// conversation's declaration. An entry is either a concrete agent name or a
	// reference (`@`-prefixed) that is resolved at scheduling time — which is why
	// a row can say `@peer` without knowing who will be speaking.
	//
	// Empty means "follow this conversation type's template", see
	// bot.defaultRefs. An empty string and `["@default"]` are therefore not the
	// same thing: the first defers to the template, the second is an explicit
	// choice that survives a template change.
	//
	// In a private chat the speaker's own binding wins over this column when it
	// names an agent: the room and the speaker are one person there, so the
	// binding the user sees in the console is the only statement that can be
	// about them. The column stays the room-level declaration for groups and the
	// fallback for an unbound (or empty-binding) private chat.
	//
	// It is the **whole** of what a conversation states about its agents. The
	// resolved result used to be recorded beside it (a former `agents` column),
	// and that was wrong for a reason no refresh rate could fix: a declaration is
	// the conversation's, but what it resolves to is the **speaker's** — in a
	// group, `@peer` is one agent for whoever bound an account and another for the
	// next person to talk. A row-wide record of it therefore answered "what did
	// the last speaker run as" while every reader was asking "what do I run as",
	// and the two coincide only in a private chat. So nothing is recorded: every
	// reader resolves through the scheduler, against the caller.
	//
	// The question "who did this particular turn run as" has its own answer in
	// agent.interaction, one row per turn.
	Refs         string     `gorm:"column:refs;not null;default:''" json:"refs"`
	LastActiveAt time.Time  `gorm:"column:last_active_at;not null;default:current_timestamp;comment:最后活跃时间" json:"last_active_at"`
	SavedAt      *time.Time `gorm:"column:saved_at;comment:会话/群保存时间" json:"saved_at"`
	ClosedAt     *time.Time `gorm:"column:closed_at;comment:报障单/会话关闭时间" json:"closed_at"`
	DismissedAt  *time.Time `gorm:"column:dismissed_at;comment:群解散时间" json:"dismissed_at"`
	CreatedAt    time.Time  `gorm:"column:created_at;not null;default:current_timestamp;comment:创建时间" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;not null;default:current_timestamp;comment:更新时间" json:"updated_at"`
}

// TableName returns table name.
func (*SessionM) TableName() string {
	return TableNameSessionM()
}

// RefList decodes Refs into a slice. A malformed or empty value yields nil
// rather than an error, for the reason OncallGroupM.AgentList gives: a torn
// cell should not take the whole conversation down, and "no refs" is a valid
// state that callers already handle.
func (m *SessionM) RefList() []string { return DecodeNameList(m.Refs) }

// EncodeNameList renders a []string as the JSON array these columns hold.
//
// An empty list becomes an empty string rather than "[]": the columns default
// to ”, and two spellings of "nothing" is one more thing every reader has to
// compare against.
func EncodeNameList(names []string) string {
	if len(names) == 0 {
		return ""
	}
	b, err := json.Marshal(names)
	if err != nil {
		// Marshalling a []string cannot fail; a caller that got here has a bug
		// it cannot act on, and an empty column is the answer that fails closed.
		return ""
	}
	return string(b)
}

// DecodeNameList decodes a JSON array of strings, treating an empty or
// malformed value as absent.
func DecodeNameList(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// AfterCreate generates a chatID after creating a database record.
func (m *SessionM) AfterCreate(tx *gorm.DB) error {
	m.ChatID = rid.NewResourceID("chat").New(uint64(m.ID))
	return tx.Save(m).Error
}

func (m *SessionM) IsClosed() bool    { return m.ClosedAt != nil }
func (m *SessionM) IsDismissed() bool { return m.DismissedAt != nil }
func (m *SessionM) IsSaved() bool     { return m.SavedAt != nil }

func init() {
	registry.Register(&SessionM{})
}
