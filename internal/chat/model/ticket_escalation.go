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
	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

// TableNameTicketEscalationM returns the physical escalation state table name.
func TableNameTicketEscalationM() string {
	return dbschema.Table(dbschema.Chat, "ticket_escalation")
}

// TicketEscalationM is the runtime state of one ticket's escalation. It is
// operational data, not configuration: creating a new state row is what starts
// an escalation, and Status is what the worker advances.
//
// There is one row per ticket: TicketID carries a unique index. A second
// Start is therefore an idempotent no-op rather than a second page.
type TicketEscalationM struct {
	ID       uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	TicketID string `gorm:"column:ticket_id;size:64;not null;uniqueIndex:uq_chat_ticket_escalation_ticket" json:"ticketId"`
	GroupID  string `gorm:"column:group_id;size:128;not null;index:idx_chat_ticket_escalation_group" json:"groupId"`

	TierIndex int64  `gorm:"column:tier_index;not null;default:0" json:"tierIndex"`
	Status    string `gorm:"column:status;size:32;not null;default:'pending';index:idx_chat_ticket_escalation_due" json:"status"`
	Attempts  int64  `gorm:"column:attempts;not null;default:0" json:"attempts"`

	// LastNotifiedAt and NextAttemptAt are unix milliseconds. The latter is the
	// column the worker scans, so it carries an index together with status.
	LastNotifiedAt int64 `gorm:"column:last_notified_at;not null;default:0" json:"lastNotifiedAt"`
	NextAttemptAt  int64 `gorm:"column:next_attempt_at;not null;default:0;index:idx_chat_ticket_escalation_due" json:"nextAttemptAt"`

	AcknowledgedAt int64  `gorm:"column:acknowledged_at;not null;default:0" json:"acknowledgedAt"`
	AcknowledgedBy string `gorm:"column:acknowledged_by;size:256;not null;default:''" json:"acknowledgedBy"`
	LastError      string `gorm:"column:last_error;type:text;not null;default:''" json:"lastError"`

	CreatedAt int64 `gorm:"column:created_at;not null;default:0" json:"createdAt"`
	UpdatedAt int64 `gorm:"column:updated_at;not null;default:0" json:"updatedAt"`
}

func (*TicketEscalationM) TableName() string { return TableNameTicketEscalationM() }

func init() { registry.Register(&TicketEscalationM{}) }
