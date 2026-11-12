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

// Rotation types. A rotation is a repeated shift pattern; the type only picks
// the default shift length, while shift_duration_seconds is authoritative.
const (
	RotationDaily  = "daily"
	RotationWeekly = "weekly"
	RotationCustom = "custom"
)

// Escalation target types.
const (
	EscalationTargetSchedule = "schedule"
	EscalationTargetUsers    = "users"
	EscalationTargetBoth     = "both"
)

// Escalation state lifecycle.
const (
	EscalationPending      = "pending"
	EscalationNotified     = "notified"
	EscalationAcknowledged = "acknowledged"
	EscalationCompleted    = "completed"
)

// IsRotationType reports whether t names a supported rotation type.
func IsRotationType(t string) bool {
	switch t {
	case RotationDaily, RotationWeekly, RotationCustom:
		return true
	default:
		return false
	}
}

// RotationTypes returns every accepted rotation type.
func RotationTypes() []string { return []string{RotationDaily, RotationWeekly, RotationCustom} }

// IsEscalationTargetType reports whether t names a supported escalation target.
func IsEscalationTargetType(t string) bool {
	switch t {
	case EscalationTargetSchedule, EscalationTargetUsers, EscalationTargetBoth:
		return true
	default:
		return false
	}
}

// TableNameOncallScheduleM returns the physical table name under the configured
// dialect: chat.oncall_schedule on PostgreSQL, onexai_chat_oncall_schedule on
// MySQL.
func TableNameOncallScheduleM() string { return dbschema.Table(dbschema.Chat, "oncall_schedule") }

// OncallScheduleM is the schedule definition of one duty group. It is 1:1 with
// OncallGroupM and is keyed by the group name rather than by a numeric FK, so a
// group rename is a deliberate migration rather than a cascading update.
//
// The schedule row owns the rotation definition and the escalation policy's
// toggle fields. The operational children (layers, participants, overrides,
// unavailabilities, holidays, escalation tiers) live in their own tables.
type OncallScheduleM struct {
	ID        uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	GroupName string `gorm:"column:group_name;size:128;not null;uniqueIndex:uq_chat_oncall_schedule_group" json:"groupName"`

	// Timezone is the IANA zone used for calendar days, unavailability dates
	// and holiday dates. Shift boundaries are absolute instants and do not use
	// this zone (see internal/chat/oncall).
	Timezone    string `gorm:"column:timezone;size:64;not null;default:'UTC'" json:"timezone"`
	Description string `gorm:"column:description;type:text;not null;default:''" json:"description"`
	// NotificationChannel is an optional chat id used for shift notifications.
	NotificationChannel string `gorm:"column:notification_channel;size:256;not null;default:''" json:"notificationChannel"`

	// EscalationEnabled turns the group's policy on for manual and automatic
	// escalation.
	EscalationEnabled bool `gorm:"column:escalation_enabled;not null;default:false" json:"escalationEnabled"`
	// EscalationAuto starts an escalation when a ticket is created and the
	// ticket's level is at most EscalationFromLevel.
	EscalationAuto bool `gorm:"column:escalation_auto;not null;default:false" json:"escalationAuto"`
	// EscalationFromLevel is the highest (numerically smallest) ticket level
	// that auto-starts an escalation. 0 disables the level gate.
	EscalationFromLevel int64 `gorm:"column:escalation_from_level;not null;default:0" json:"escalationFromLevel"`

	CreatedAt int64 `gorm:"column:created_at;not null;default:0" json:"createdAt"`
	UpdatedAt int64 `gorm:"column:updated_at;not null;default:0" json:"updatedAt"`
}

func (*OncallScheduleM) TableName() string { return TableNameOncallScheduleM() }

// TableNameOncallScheduleLayerM returns the physical layer table name.
func TableNameOncallScheduleLayerM() string {
	return dbschema.Table(dbschema.Chat, "oncall_schedule_layer")
}

// OncallScheduleLayerM is one rotation layer. Layers are ordered and evaluated
// as a fallback chain: the first layer that yields an available participant
// wins.
type OncallScheduleLayerM struct {
	ID         uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	GroupName  string `gorm:"column:group_name;size:128;not null;index:idx_chat_oncall_layer_group;uniqueIndex:uq_chat_oncall_layer_order" json:"groupName"`
	Name       string `gorm:"column:name;size:128;not null" json:"name"`
	OrderIndex int64  `gorm:"column:order_index;not null;default:0;uniqueIndex:uq_chat_oncall_layer_order" json:"orderIndex"`

	RotationType         string    `gorm:"column:rotation_type;size:16;not null" json:"rotationType"`
	RotationStart        time.Time `gorm:"column:rotation_start;not null" json:"rotationStart"`
	ShiftDurationSeconds int64     `gorm:"column:shift_duration_seconds;not null" json:"shiftDurationSeconds"`

	CreatedAt int64 `gorm:"column:created_at;not null;default:0" json:"createdAt"`
}

func (*OncallScheduleLayerM) TableName() string { return TableNameOncallScheduleLayerM() }

// TableNameOncallScheduleParticipantM returns the physical participant table
// name.
func TableNameOncallScheduleParticipantM() string {
	return dbschema.Table(dbschema.Chat, "oncall_schedule_participant")
}

// OncallScheduleParticipantM is one person in a layer's rotation. UserName is
// free text (usually an email), matching the duty commands and the Regen
// provider's contract.
type OncallScheduleParticipantM struct {
	ID         uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	LayerID    uint64 `gorm:"column:layer_id;not null;index:idx_chat_oncall_participant_layer;uniqueIndex:uq_chat_oncall_participant_order" json:"layerId"`
	UserName   string `gorm:"column:user_name;size:256;not null" json:"userName"`
	OrderIndex int64  `gorm:"column:order_index;not null;default:0;uniqueIndex:uq_chat_oncall_participant_order" json:"orderIndex"`
	CreatedAt  int64  `gorm:"column:created_at;not null;default:0" json:"createdAt"`
}

func (*OncallScheduleParticipantM) TableName() string {
	return TableNameOncallScheduleParticipantM()
}

// TableNameOncallScheduleOverrideM returns the physical override table name.
func TableNameOncallScheduleOverrideM() string {
	return dbschema.Table(dbschema.Chat, "oncall_schedule_override")
}

// OncallScheduleOverrideM replaces the rotation for a time window. An override
// is schedule-wide and takes precedence over every layer.
type OncallScheduleOverrideM struct {
	ID           string    `gorm:"column:id;size:36;primaryKey" json:"id"`
	GroupName    string    `gorm:"column:group_name;size:128;not null;index:idx_chat_oncall_override_group" json:"groupName"`
	OverrideUser string    `gorm:"column:override_user;size:256;not null" json:"overrideUser"`
	StartTime    time.Time `gorm:"column:start_time;not null;index:idx_chat_oncall_override_window" json:"startTime"`
	EndTime      time.Time `gorm:"column:end_time;not null;index:idx_chat_oncall_override_window" json:"endTime"`
	CreatedBy    string    `gorm:"column:created_by;size:256;not null;default:''" json:"createdBy"`
	CreatedAt    int64     `gorm:"column:created_at;not null;default:0" json:"createdAt"`
}

func (*OncallScheduleOverrideM) TableName() string { return TableNameOncallScheduleOverrideM() }

// TableNameOncallScheduleUnavailabilityM returns the physical unavailability
// table name.
func TableNameOncallScheduleUnavailabilityM() string {
	return dbschema.Table(dbschema.Chat, "oncall_schedule_unavailability")
}

// OncallScheduleUnavailabilityM marks a participant unavailable for whole days
// in the schedule's timezone. StartDate and EndDate are inclusive YYYY-MM-DD
// values.
type OncallScheduleUnavailabilityM struct {
	ID        string `gorm:"column:id;size:36;primaryKey" json:"id"`
	GroupName string `gorm:"column:group_name;size:128;not null;index:idx_chat_oncall_unavailable_group" json:"groupName"`
	UserName  string `gorm:"column:user_name;size:256;not null" json:"userName"`
	StartDate string `gorm:"column:start_date;size:10;not null" json:"startDate"`
	EndDate   string `gorm:"column:end_date;size:10;not null" json:"endDate"`
	Reason    string `gorm:"column:reason;type:text;not null;default:''" json:"reason"`
	CreatedBy string `gorm:"column:created_by;size:256;not null;default:''" json:"createdBy"`
	CreatedAt int64  `gorm:"column:created_at;not null;default:0" json:"createdAt"`
}

func (*OncallScheduleUnavailabilityM) TableName() string {
	return TableNameOncallScheduleUnavailabilityM()
}

// TableNameOncallScheduleHolidayM returns the physical holiday table name.
func TableNameOncallScheduleHolidayM() string {
	return dbschema.Table(dbschema.Chat, "oncall_schedule_holiday")
}

// OncallScheduleHolidayM is a manually maintained holiday marker. Holidays are
// display-only in V1; they do not change rotation math.
type OncallScheduleHolidayM struct {
	ID          string `gorm:"column:id;size:36;primaryKey" json:"id"`
	GroupName   string `gorm:"column:group_name;size:128;not null;index:idx_chat_oncall_holiday_group" json:"groupName"`
	Date        string `gorm:"column:date;size:10;not null" json:"date"`
	Name        string `gorm:"column:name;size:256;not null" json:"name"`
	CountryCode string `gorm:"column:country_code;size:8;not null;default:''" json:"countryCode"`
	CreatedAt   int64  `gorm:"column:created_at;not null;default:0" json:"createdAt"`
}

func (*OncallScheduleHolidayM) TableName() string { return TableNameOncallScheduleHolidayM() }

// TableNameOncallEscalationTierM returns the physical escalation tier table
// name.
func TableNameOncallEscalationTierM() string {
	return dbschema.Table(dbschema.Chat, "oncall_escalation_tier")
}

// OncallEscalationTierM is one step of a group's escalation policy.
type OncallEscalationTierM struct {
	ID             uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	GroupName      string `gorm:"column:group_name;size:128;not null;index:idx_chat_oncall_tier_group;uniqueIndex:uq_chat_oncall_tier_order" json:"groupName"`
	TierIndex      int64  `gorm:"column:tier_index;not null;default:0;uniqueIndex:uq_chat_oncall_tier_order" json:"tierIndex"`
	TimeoutSeconds int64  `gorm:"column:timeout_seconds;not null;default:300" json:"timeoutSeconds"`
	TargetType     string `gorm:"column:target_type;size:16;not null;default:'schedule'" json:"targetType"`
	// TargetGroupName is the schedule to resolve for a schedule target. Empty
	// means "the group this policy belongs to".
	TargetGroupName string `gorm:"column:target_group_name;size:128;not null;default:''" json:"targetGroupName"`
	// UserNames is a JSON array of free-text names, decoded by UserList.
	UserNames string `gorm:"column:user_names;type:text;not null;default:'[]'" json:"userNames"`
	CreatedAt int64  `gorm:"column:created_at;not null;default:0" json:"createdAt"`
}

func (*OncallEscalationTierM) TableName() string { return TableNameOncallEscalationTierM() }

// UserList decodes UserNames. A malformed value yields nil, matching the
// leniency of the other JSON-in-text columns in this schema.
func (m *OncallEscalationTierM) UserList() []string { return DecodeNameList(m.UserNames) }

// OncallScheduleLayerAggregate is one layer together with its participants.
type OncallScheduleLayerAggregate struct {
	Layer        OncallScheduleLayerM
	Participants []*OncallScheduleParticipantM
}

// OncallScheduleDefinition is the writable rotation definition: the schedule
// header plus its layers and participants. Participants is keyed by the index
// of the layer in Layers; the store assigns layer IDs inside its transaction.
type OncallScheduleDefinition struct {
	Schedule     *OncallScheduleM
	Layers       []*OncallScheduleLayerM
	Participants map[int][]*OncallScheduleParticipantM
}

// OncallScheduleAggregate is the storage-independent view the evaluator needs.
// A nil schedule means the group has no native schedule configured.
type OncallScheduleAggregate struct {
	Schedule         *OncallScheduleM
	Layers           []OncallScheduleLayerAggregate
	Overrides        []*OncallScheduleOverrideM
	Unavailabilities []*OncallScheduleUnavailabilityM
	Holidays         []*OncallScheduleHolidayM
	Tiers            []*OncallEscalationTierM
}

func init() {
	registry.Register(&OncallScheduleM{})
	registry.Register(&OncallScheduleLayerM{})
	registry.Register(&OncallScheduleParticipantM{})
	registry.Register(&OncallScheduleOverrideM{})
	registry.Register(&OncallScheduleUnavailabilityM{})
	registry.Register(&OncallScheduleHolidayM{})
	registry.Register(&OncallEscalationTierM{})
}
