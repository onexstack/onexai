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

// TableNameTicketNoteM returns the physical table name under the configured
// dialect: chat.ticket_note on PostgreSQL, onexai_chat_ticket_note on MySQL.
func TableNameTicketNoteM() string { return dbschema.Table(dbschema.Chat, "ticket_note") }

// TicketNoteM is one handling note on a ticket (处理记录, handling note), written by /mark.
//
// It is append-only and one row per /mark invocation rather than a single
// growing text column on the ticket: the note text exists nowhere else (the
// group chat does not echo it), so a row is the only record of who said what
// and when. Review and Demand are the two marks a /mark may carry; they sit on
// the row that carried them rather than as flags on the ticket, so marking
// twice leaves a trail instead of overwriting a boolean.
type TicketNoteM struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement:true;comment:主键 ID" json:"id"`
	// TicketID is the ticket's business key (TicketM.TicketID), the same
	// identity a session links to.
	TicketID   string `gorm:"column:ticket_id;not null;index;comment:工单 ID" json:"ticket_id"`
	AuthorID   string `gorm:"column:author_id;not null;default:'';comment:记录人 ID" json:"author_id"`
	AuthorName string `gorm:"column:author_name;not null;default:'';comment:记录人名称" json:"author_name"`
	// Note is the free-form text of this mark.
	Note string `gorm:"column:note;type:text;not null;comment:处理备注" json:"note"`
	// Review marks this note as a "needs a retrospective" (复盘, retrospective) request.
	Review bool `gorm:"column:review;not null;default:false;comment:是否标记复盘" json:"review"`
	// Demand marks this note as turning the ticket into a product demand (需求, demand).
	Demand    bool      `gorm:"column:demand;not null;default:false;comment:是否标记需求" json:"demand"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:current_timestamp;comment:创建时间" json:"created_at"`
}

// TableName returns table name.
func (*TicketNoteM) TableName() string {
	return TableNameTicketNoteM()
}

func init() {
	registry.Register(&TicketNoteM{})
}
