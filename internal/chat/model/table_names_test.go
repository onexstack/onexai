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
	"testing"

	"github.com/onexstack/onexai/pkg/dbschema"
)

// TestTableNamesResolvePerDialect pins both physical names of every chat
// table. The names were renamed twice: once from the shared oai_* / unprefixed
// names to make ownership obvious, and once again when PostgreSQL split the
// tables across per-service schemas. A drifted name means AutoMigrate creates
// an empty table and the real data becomes unreachable, so this test is what
// stops that.
func TestTableNamesResolvePerDialect(t *testing.T) {
	bare := map[string]string{
		"session":           "session",
		"ticket":            "ticket",
		"ticket_note":       "ticket_note",
		"chat_message":      "chat_message",
		"metadata":          "metadata",
		"oncall_group":      "oncall_group",
		"agent_command":     "agent_command",
		"message_record":    "message_record",
		"peer_agent":        "peer_agent",
		"peer_bind_claim":   "peer_bind_claim",
		"user":              "user",
		"user_lark_binding": "user_lark_binding",
		"user_bind_code":    "user_bind_code",

		"oncall_schedule":                "oncall_schedule",
		"oncall_schedule_layer":          "oncall_schedule_layer",
		"oncall_schedule_participant":    "oncall_schedule_participant",
		"oncall_schedule_override":       "oncall_schedule_override",
		"oncall_schedule_unavailability": "oncall_schedule_unavailability",
		"oncall_schedule_holiday":        "oncall_schedule_holiday",
		"oncall_escalation_tier":         "oncall_escalation_tier",
		"ticket_escalation":              "ticket_escalation",
	}
	tables := map[string]func() string{
		"session":           TableNameSessionM,
		"ticket":            TableNameTicketM,
		"ticket_note":       TableNameTicketNoteM,
		"chat_message":      TableNameChatMessageM,
		"metadata":          TableNameMetadataM,
		"oncall_group":      TableNameOncallGroupM,
		"message_record":    TableNameMessageM,
		"peer_agent":        TableNamePeerAgentM,
		"peer_bind_claim":   TableNamePeerBindClaimM,
		"user":              TableNameUserM,
		"user_lark_binding": TableNameUserLarkBindingM,
		"user_bind_code":    TableNameUserBindCodeM,

		"oncall_schedule":                TableNameOncallScheduleM,
		"oncall_schedule_layer":          TableNameOncallScheduleLayerM,
		"oncall_schedule_participant":    TableNameOncallScheduleParticipantM,
		"oncall_schedule_override":       TableNameOncallScheduleOverrideM,
		"oncall_schedule_unavailability": TableNameOncallScheduleUnavailabilityM,
		"oncall_schedule_holiday":        TableNameOncallScheduleHolidayM,
		"oncall_escalation_tier":         TableNameOncallEscalationTierM,
		"ticket_escalation":              TableNameTicketEscalationM,
	}

	t.Cleanup(func() { dbschema.Configure(dbschema.MySQL) })

	dbschema.Configure(dbschema.MySQL)
	for what, table := range tables {
		if got, want := table(), "onexai_chat_"+bare[what]; got != want {
			t.Errorf("%s table name on mysql = %q, want %q", what, got, want)
		}
	}

	dbschema.Configure(dbschema.Postgres)
	for what, table := range tables {
		if got, want := table(), "chat."+bare[what]; got != want {
			t.Errorf("%s table name on postgres = %q, want %q", what, got, want)
		}
	}
}
