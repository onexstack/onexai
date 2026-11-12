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

func init() { registry.Register(&InteractionM{}) }

// InteractionM is one user↔assistant turn, recorded for security auditing. It
// mirrors the opsassist InteractionM table (column names kept verbatim).
type InteractionM struct {
	ID            uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	InteractionID string `gorm:"uniqueIndex;size:64" json:"interaction_id"` // uuid
	Source        string `gorm:"size:20;index" json:"source"`               // lark | mcp | api
	CallerKey     string `gorm:"size:100" json:"caller_key"`                // HTTP caller AK/SK secretID (api only)
	CallerName    string `gorm:"size:255" json:"caller_name"`               // HTTP caller service name
	ChatType      string `gorm:"size:20" json:"chat_type"`                  // p2p | group
	SessionType   string `gorm:"size:50" json:"session_type"`               // ticket_group | normal_group | private_chat | openapi
	ChatID        string `gorm:"size:100" json:"chat_id"`
	SenderID      string `gorm:"size:100;index" json:"sender_id"`
	SenderName    string `gorm:"size:100" json:"sender_name"`
	IsSRE         bool   `gorm:"not null;default:0" json:"is_sre"`
	// AgentNames is the Agent selection this turn ran with, as the surface
	// requested it. It is what makes the row answer "which agent answered this",
	// which no other column can: the conversation's own record of its selection
	// is a current-state column, overwritten by the next turn, so the history of
	// who ran when is only here.
	//
	// Empty means the turn pinned no Agent and ran the engine's built-in
	// assistant — a real answer, and the one the OpenAI-compatible surface
	// produces when its caller names no model.
	AgentNames   StringList `gorm:"type:text" json:"agent_names"`
	UserInput    string     `gorm:"type:text;not null" json:"user_input"`
	FinalAnswer  string     `gorm:"type:text;not null" json:"final_answer"`
	TotalMs      int64      `gorm:"not null;default:0" json:"total_ms"`
	Status       string     `gorm:"size:32" json:"status"` // success | error | stopped | blocked
	ErrorMessage string     `gorm:"type:text;not null" json:"error_message"`
	CreatedAt    time.Time  `gorm:"not null;default:current_timestamp" json:"created_at"`
}

// TableName returns the interaction table name.
func (InteractionM) TableName() string { return dbschema.Table(dbschema.Agent, "interaction") }
