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

// TableNameTicketM returns the physical table name under the configured
// dialect: chat.ticket on PostgreSQL, onexai_chat_ticket on MySQL.
func TableNameTicketM() string { return dbschema.Table(dbschema.Chat, "ticket") }

// TicketStatus is the lifecycle state of a ticket. The zero value is empty; use
// the TicketStatusXxx constants below.
type TicketStatus string

const (
	// TicketStatusOpen marks a newly created ticket awaiting assignment.
	TicketStatusOpen TicketStatus = "open"
	// TicketStatusProcessing marks a ticket actively being worked on.
	TicketStatusProcessing TicketStatus = "processing"
	// TicketStatusSaved marks a ticket that has been set aside for later.
	TicketStatusSaved TicketStatus = "saved"
	// TicketStatusResolved marks a ticket whose issue has been resolved.
	TicketStatusResolved TicketStatus = "resolved"
	// TicketStatusClosed marks a finished/closed ticket.
	TicketStatusClosed TicketStatus = "closed"
)

// ticketStatuses is the closed set of statuses a ticket may hold, keyed by the
// constant so that adding one above is the only edit a new status needs.
var ticketStatuses = map[TicketStatus]struct{}{
	TicketStatusOpen:       {},
	TicketStatusProcessing: {},
	TicketStatusSaved:      {},
	TicketStatusResolved:   {},
	TicketStatusClosed:     {},
}

// IsTicketStatus reports whether s names a status a ticket may hold.
//
// It exists so that a caller which filters by status — the on-call read surface
// takes a list of them — rejects a misspelling instead of answering an empty
// page. An empty page is a legitimate answer, so a typo that produced one would
// be indistinguishable from "there is nothing to do".
func IsTicketStatus(s string) bool {
	_, ok := ticketStatuses[TicketStatus(s)]
	return ok
}

// TicketStatuses returns every accepted status, in lifecycle order, for an
// error message that has to list them.
func TicketStatuses() []string {
	return []string{
		string(TicketStatusOpen),
		string(TicketStatusProcessing),
		string(TicketStatusSaved),
		string(TicketStatusResolved),
		string(TicketStatusClosed),
	}
}

// TicketM stores metadata for ticket records locally.
//
// The name says "ticket" because that is the product's word for it (工单 = ticket); the
// row is an on-call incident, and IncidentID is the handle it carries on the
// incident platform it is mirrored to.
type TicketM struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement:true;comment:主键 ID" json:"id"`
	// TicketID is this system's own, stable business key (工单 ID, ticket ID). It is what
	// sessions link to and what every command addresses a ticket by, so it must
	// not change for the lifetime of the ticket — see IncidentID.
	TicketID    string       `gorm:"column:ticket_id;not null;uniqueIndex;comment:工单 ID" json:"ticket_id"`
	Title       string       `gorm:"column:title;not null;default:'';comment:工单标题" json:"title"`
	Description string       `gorm:"column:description;type:text;not null;comment:工单描述" json:"description"`
	Status      TicketStatus `gorm:"column:status;not null;default:'open';comment:工单状态" json:"status"`
	// Level is the ticket level: 1 is the most urgent, 4 the least.
	Level int64 `gorm:"column:level;not null;default:0;comment:工单级别(1 最高)" json:"level"`
	// GroupID is the duty group the ticket is routed to (值班组 ID, duty group ID), the same
	// token the duty commands take as an argument and oncall_group is keyed by.
	GroupID string `gorm:"column:group_id;not null;default:'';index;comment:值班组 ID" json:"group_id"`
	// ReporterID / ReporterName identify who raised the ticket (报障人, reporter).
	ReporterID   string `gorm:"column:reporter_id;not null;default:'';comment:报障人 ID" json:"reporter_id"`
	ReporterName string `gorm:"column:reporter_name;not null;default:'';comment:报障人名称" json:"reporter_name"`
	// ReporterOpenID is the reporter's app-scoped Lark identity (open_id), and
	// it sits beside ReporterID rather than replacing it: the two are different
	// namespaces, not two spellings of one. ReporterID is the tenant-scoped
	// user_id — the id this system keys a person by (see larkkit.Identity) —
	// and it is **empty for anyone outside this tenant**, while an open_id is
	// always present. That asymmetry is what this column is for: it is the only
	// handle that exists for an outsider, so it is the only one an external
	// system (OneX binds people by open_id) can be asked about.
	//
	// # It must not be added to hack/identity-backfill's targets
	//
	// That tool resolves the *tenants* ids on columns that "may still hold their
	// open_id" and rewrites them to user_id. This column is the opposite: its
	// contract is open_id, so listing it there would silently rewrite exactly
	// the values its readers depend on, and nothing would report it.
	ReporterOpenID string `gorm:"column:reporter_open_id;size:64;not null;default:'';comment:报障人 open_id" json:"reporter_open_id"`
	// AssigneeID / AssigneeName identify who currently owns the ticket
	// (处理人, assignee). /assign hands it to a person, /hand to another group's on-call.
	AssigneeID   string `gorm:"column:assignee_id;not null;default:'';index;comment:处理人 ID" json:"assignee_id"`
	AssigneeName string `gorm:"column:assignee_name;not null;default:'';comment:处理人名称" json:"assignee_name"`

	// IncidentID is the id of the incident this ticket is mirrored to on the
	// Regen on-call platform (Regen 事件 ID, Regen incident ID), paired with Source. It is *not*
	// this system's key: Regen's incident state machine is one-way, so /open
	// reopens by creating a fresh incident and replaces this value, while
	// TicketID stays put. Empty means the ticket has no remote twin, which is
	// the case when Regen is not configured.
	IncidentID string `gorm:"column:incident_id;type:varchar(64);not null;default:'';comment:regen 事件 ID" json:"incident_id"`
	// Source is the ticket origin (工单来源, ticket source), e.g. 报障单系统 (the incident-reporting system) or "regen".
	Source string `gorm:"column:source;not null;default:'';comment:工单来源" json:"source"`

	// Summary is the closing write-up (结案总结, closing summary): what this ticket was about and how
	// it was resolved, written by a model when the ticket closes.
	//
	// # Why it is a column here and not a row in chat.ticket_note
	//
	// A ticket has exactly one of these, and regenerating it *replaces* it — so it
	// is the ticket's state, in the same sense as Status and ClosedAt, rather than
	// an event. The note table's contract is the opposite and says so in its own
	// comment: append-only, one row per /mark invocation, because a note's text
	// exists nowhere else and the row is the only record of who said what and when.
	// A generated artifact in that log would be read as something a person wrote,
	// and there is no honest value for its AuthorID.
	//
	// # Empty does not mean "nothing to say"
	//
	// Empty means **not generated yet**, and it covers success-to-be, failure, and
	// "the process died between closing and summarizing" alike — see
	// MarkOptions.Run's sibling, the /close summary path, for why the design accepts
	// that instead of carrying a state column. A ticket whose content is genuinely
	// not worth a write-up gets the model's brief marker as its text, not an empty
	// string: the difference between "we looked and it was not worth recording" and
	// "nobody looked" is one an operator asks about.
	Summary string `gorm:"column:summary;type:text;not null;default:'';comment:结案总结" json:"summary"`

	// ResolvedAt / ClosedAt are set when the ticket reaches the matching status.
	ResolvedAt *time.Time `gorm:"column:resolved_at;comment:实际解决时间" json:"resolved_at"`
	ClosedAt   *time.Time `gorm:"column:closed_at;comment:关闭时间" json:"closed_at"`
	CreatedAt  time.Time  `gorm:"column:created_at;not null;default:current_timestamp;comment:创建时间" json:"created_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;not null;default:current_timestamp;comment:更新时间" json:"updated_at"`
}

// TableName returns table name.
func (*TicketM) TableName() string {
	return TableNameTicketM()
}

func init() {
	registry.Register(&TicketM{})
}
