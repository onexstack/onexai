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

package api

import "time"

// The native on-call schedule wire types. Field names are camelCase, matching
// the group resource the console already consumes. Times are RFC 3339 instants;
// dates are YYYY-MM-DD strings interpreted in the schedule timezone.

// OncallScheduleDefinition is the writable rotation definition. It is also the
// body of PUT /schedule: operational children (overrides, unavailabilities,
// holidays) have their own endpoints and are never replaced by this request.
type OncallScheduleDefinition struct {
	Timezone            string                `json:"timezone"`
	Description         string                `json:"description"`
	NotificationChannel string                `json:"notificationChannel"`
	Layers              []OncallScheduleLayer `json:"layers"`
}

// OncallScheduleLayer is one rotation layer.
type OncallScheduleLayer struct {
	Name                 string                      `json:"name"`
	OrderIndex           int64                       `json:"orderIndex"`
	RotationType         string                      `json:"rotationType"`
	RotationStart        time.Time                   `json:"rotationStart"`
	ShiftDurationSeconds int64                       `json:"shiftDurationSeconds"`
	Participants         []OncallScheduleParticipant `json:"participants"`
}

// OncallScheduleParticipant is one person in a layer's rotation.
type OncallScheduleParticipant struct {
	UserName   string `json:"userName"`
	OrderIndex int64  `json:"orderIndex"`
}

// OncallSchedule is the read shape: the definition plus the operational
// children.
type OncallSchedule struct {
	OncallScheduleDefinition
	Overrides        []OncallScheduleOverride       `json:"overrides"`
	Unavailabilities []OncallScheduleUnavailability `json:"unavailabilities"`
	Holidays         []OncallScheduleHoliday        `json:"holidays"`
}

// OncallScheduleOverride replaces the rotation for a window.
type OncallScheduleOverride struct {
	ID           string    `json:"id"`
	OverrideUser string    `json:"overrideUser"`
	StartTime    time.Time `json:"startTime"`
	EndTime      time.Time `json:"endTime"`
	CreatedBy    string    `json:"createdBy,omitempty"`
	CreatedAt    time.Time `json:"createdAt,omitempty"`
}

// OncallScheduleUnavailability marks a participant unavailable for whole days.
type OncallScheduleUnavailability struct {
	ID        string `json:"id"`
	UserName  string `json:"userName"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Reason    string `json:"reason,omitempty"`
	CreatedBy string `json:"createdBy,omitempty"`
}

// OncallScheduleHoliday is a display-only holiday marker.
type OncallScheduleHoliday struct {
	ID          string `json:"id"`
	Date        string `json:"date"`
	Name        string `json:"name"`
	CountryCode string `json:"countryCode,omitempty"`
}

// OncallScheduleResponse is the GET/PUT /schedule response.
type OncallScheduleResponse struct {
	Group            string          `json:"group"`
	Configured       bool            `json:"configured"`
	Source           string          `json:"source"`
	LegacyScheduleID string          `json:"legacyScheduleId,omitempty"`
	Schedule         *OncallSchedule `json:"schedule,omitempty"`
}

// OncallCurrentMember is one person on call.
type OncallCurrentMember struct {
	UserName   string     `json:"userName"`
	IsOverride bool       `json:"isOverride"`
	LayerName  string     `json:"layerName,omitempty"`
	Since      *time.Time `json:"since,omitempty"`
	Until      *time.Time `json:"until,omitempty"`
}

// OncallCurrentResponse is GET /schedule/oncall and one item of
// GET /oncall-current.
type OncallCurrentResponse struct {
	Group    string                `json:"group"`
	At       time.Time             `json:"at"`
	Source   string                `json:"source"`
	Timezone string                `json:"timezone,omitempty"`
	Current  *OncallCurrentMember  `json:"current,omitempty"`
	Backups  []OncallCurrentMember `json:"backups,omitempty"`
}

// ListOncallCurrentResponse is GET /oncall-current.
type ListOncallCurrentResponse struct {
	Items []*OncallCurrentResponse `json:"items"`
}

// OncallSegment is one interval of a timeline.
type OncallSegment struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	UserName   string    `json:"userName"`
	IsOverride bool      `json:"isOverride"`
	LayerName  string    `json:"layerName,omitempty"`
}

// OncallTimelineResponse is GET /schedule/timeline.
type OncallTimelineResponse struct {
	Group    string          `json:"group"`
	From     time.Time       `json:"from"`
	To       time.Time       `json:"to"`
	Timezone string          `json:"timezone"`
	Segments []OncallSegment `json:"segments"`
}

// OncallLayerTimelinesResponse is GET /schedule/layer-timelines. Layers is
// keyed by layer name.
type OncallLayerTimelinesResponse struct {
	Group     string                     `json:"group"`
	From      time.Time                  `json:"from"`
	To        time.Time                  `json:"to"`
	Timezone  string                     `json:"timezone"`
	Layers    map[string][]OncallSegment `json:"layers"`
	Effective []OncallSegment            `json:"effective"`
}

// OncallEscalationTier is one step of a group's escalation policy.
type OncallEscalationTier struct {
	Index          int64    `json:"index"`
	TimeoutSeconds int64    `json:"timeoutSeconds"`
	TargetType     string   `json:"targetType"`
	GroupName      string   `json:"groupName,omitempty"`
	UserNames      []string `json:"userNames,omitempty"`
}

// OncallEscalationPolicy is a group's escalation policy.
type OncallEscalationPolicy struct {
	Enabled               bool                   `json:"enabled"`
	AutoEscalate          bool                   `json:"autoEscalate"`
	AutoEscalateFromLevel int64                  `json:"autoEscalateFromLevel"`
	Tiers                 []OncallEscalationTier `json:"tiers"`
}

// OncallEscalationResponse is the GET/PUT /escalation response.
type OncallEscalationResponse struct {
	Group      string                  `json:"group"`
	Configured bool                    `json:"configured"`
	Policy     *OncallEscalationPolicy `json:"policy,omitempty"`
}

// EscalationTarget is one person paged by an escalation step.
type EscalationTarget struct {
	UserName string `json:"userName"`
	UserID   string `json:"userId,omitempty"`
	Source   string `json:"source"`
}

// EscalateResponse is POST /escalate.
type EscalateResponse struct {
	TicketID  string             `json:"ticketId"`
	Group     string             `json:"group"`
	TierIndex int64              `json:"tierIndex"`
	Status    string             `json:"status"`
	Targets   []EscalationTarget `json:"targets"`
}

// GetOncallScheduleRequest names one group.
type GetOncallScheduleRequest struct {
	Group string `uri:"name"`
}

// SaveOncallScheduleRequest is the PUT /schedule request.
type SaveOncallScheduleRequest struct {
	Group string `uri:"name" json:"-"`
	OncallScheduleDefinition
}

// PreviewOncallScheduleRequest evaluates a definition without saving it.
type PreviewOncallScheduleRequest struct {
	Group string    `uri:"name" json:"-"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	OncallScheduleDefinition
}

// GetOncallCurrentRequest is GET /schedule/oncall.
type GetOncallCurrentRequest struct {
	Group  string `uri:"name"`
	At     string `form:"at"`
	AtTime time.Time
}

// ListOncallCurrentRequest is GET /oncall-current.
type ListOncallCurrentRequest struct {
	Names     string `form:"names"`
	NamesList []string
}

// GetOncallTimelineRequest is GET /schedule/timeline.
type GetOncallTimelineRequest struct {
	Group    string `uri:"name"`
	From     string `form:"from"`
	To       string `form:"to"`
	FromTime time.Time
	ToTime   time.Time
}

// GetOncallLayerTimelinesRequest is GET /schedule/layer-timelines.
type GetOncallLayerTimelinesRequest struct {
	Group    string `uri:"name"`
	From     string `form:"from"`
	To       string `form:"to"`
	FromTime time.Time
	ToTime   time.Time
}

// ListOncallOverridesRequest is GET /schedule/overrides.
type ListOncallOverridesRequest struct {
	Group string `uri:"name"`
}

// CreateOncallOverrideRequest is POST /schedule/overrides.
type CreateOncallOverrideRequest struct {
	Group        string    `uri:"name" json:"-"`
	OverrideUser string    `json:"overrideUser"`
	StartTime    time.Time `json:"startTime"`
	EndTime      time.Time `json:"endTime"`
	CreatedBy    string    `json:"-"`
}

// DeleteOncallOverrideRequest is DELETE /schedule/overrides/:overrideID.
type DeleteOncallOverrideRequest struct {
	Group      string `uri:"name"`
	OverrideID string `uri:"overrideID"`
}

// ListOncallUnavailabilitiesRequest is GET /schedule/unavailabilities.
type ListOncallUnavailabilitiesRequest struct {
	Group string `uri:"name"`
}

// CreateOncallUnavailabilityRequest is POST /schedule/unavailabilities.
type CreateOncallUnavailabilityRequest struct {
	Group     string `uri:"name" json:"-"`
	UserName  string `json:"userName"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Reason    string `json:"reason"`
	CreatedBy string `json:"-"`
}

// DeleteOncallUnavailabilityRequest is DELETE /schedule/unavailabilities/:id.
type DeleteOncallUnavailabilityRequest struct {
	Group            string `uri:"name"`
	UnavailabilityID string `uri:"unavailabilityID"`
}

// ListOncallHolidaysRequest is GET /schedule/holidays.
type ListOncallHolidaysRequest struct {
	Group string `uri:"name"`
}

// CreateOncallHolidayRequest is POST /schedule/holidays.
type CreateOncallHolidayRequest struct {
	Group       string `uri:"name" json:"-"`
	Date        string `json:"date"`
	Name        string `json:"name"`
	CountryCode string `json:"countryCode"`
}

// DeleteOncallHolidayRequest is DELETE /schedule/holidays/:id.
type DeleteOncallHolidayRequest struct {
	Group     string `uri:"name"`
	HolidayID string `uri:"holidayID"`
}

// GetOncallEscalationRequest is GET /escalation.
type GetOncallEscalationRequest struct {
	Group string `uri:"name"`
}

// SaveOncallEscalationRequest is PUT /escalation.
type SaveOncallEscalationRequest struct {
	Group string `uri:"name" json:"-"`
	OncallEscalationPolicy
}

// EscalateRequest is POST /escalate.
type EscalateRequest struct {
	Group    string `uri:"name" json:"-"`
	TicketID string `json:"ticketId"`
}

// OverrideListResponse wraps the override list.
type OverrideListResponse struct {
	Overrides []OncallScheduleOverride `json:"overrides"`
}

// UnavailabilityListResponse wraps the unavailability list.
type UnavailabilityListResponse struct {
	Unavailabilities []OncallScheduleUnavailability `json:"unavailabilities"`
}

// HolidayListResponse wraps the holiday list.
type HolidayListResponse struct {
	Holidays []OncallScheduleHoliday `json:"holidays"`
}
