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

package validation

import (
	"context"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

const (
	maxLayers               = 10
	maxParticipants         = 200
	maxParticipantsPerLayer = 50
	maxEscalationTiers      = 10
	maxWindow               = 366 * 24 * time.Hour
)

// ValidateGetOncallScheduleRequest checks the group name.
func (v *Validator) ValidateGetOncallScheduleRequest(_ context.Context, rq *api.GetOncallScheduleRequest) error {
	return requireGroup(rq.Group)
}

// ValidateSaveOncallScheduleRequest validates a whole rotation definition.
func (v *Validator) ValidateSaveOncallScheduleRequest(_ context.Context, rq *api.SaveOncallScheduleRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	return validateScheduleDefinition(&rq.OncallScheduleDefinition)
}

// ValidatePreviewOncallScheduleRequest validates a preview request.
func (v *Validator) ValidatePreviewOncallScheduleRequest(_ context.Context, rq *api.PreviewOncallScheduleRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	if err := validateWindow(rq.From, rq.To); err != nil {
		return err
	}
	return validateScheduleDefinition(&rq.OncallScheduleDefinition)
}

// ValidateGetOncallCurrentRequest parses the optional instant.
func (v *Validator) ValidateGetOncallCurrentRequest(_ context.Context, rq *api.GetOncallCurrentRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	if strings.TrimSpace(rq.At) == "" {
		rq.AtTime = time.Now().UTC()
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, rq.At)
	if err != nil {
		return errno.ErrInvalidArgument.WithMessage("at must be RFC 3339, got %q", rq.At)
	}
	rq.AtTime = parsed.UTC()
	return nil
}

// ValidateListOncallCurrentRequest parses the optional names filter.
func (v *Validator) ValidateListOncallCurrentRequest(_ context.Context, rq *api.ListOncallCurrentRequest) error {
	rq.NamesList = splitCSV(rq.Names)
	return nil
}

// ValidateGetOncallTimelineRequest parses the window.
func (v *Validator) ValidateGetOncallTimelineRequest(_ context.Context, rq *api.GetOncallTimelineRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	from, to, err := parseWindow(rq.From, rq.To)
	if err != nil {
		return err
	}
	rq.FromTime, rq.ToTime = from, to
	return nil
}

// ValidateGetOncallLayerTimelinesRequest parses the window.
func (v *Validator) ValidateGetOncallLayerTimelinesRequest(_ context.Context, rq *api.GetOncallLayerTimelinesRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	from, to, err := parseWindow(rq.From, rq.To)
	if err != nil {
		return err
	}
	rq.FromTime, rq.ToTime = from, to
	return nil
}

// ValidateListOncallOverridesRequest checks the group name.
func (v *Validator) ValidateListOncallOverridesRequest(_ context.Context, rq *api.ListOncallOverridesRequest) error {
	return requireGroup(rq.Group)
}

// ValidateCreateOncallOverrideRequest validates an override window.
func (v *Validator) ValidateCreateOncallOverrideRequest(_ context.Context, rq *api.CreateOncallOverrideRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	rq.OverrideUser = strings.TrimSpace(rq.OverrideUser)
	if rq.OverrideUser == "" {
		return errno.ErrInvalidArgument.WithMessage("overrideUser is required")
	}
	if rq.StartTime.IsZero() || rq.EndTime.IsZero() {
		return errno.ErrInvalidArgument.WithMessage("startTime and endTime are required")
	}
	rq.StartTime = rq.StartTime.UTC()
	rq.EndTime = rq.EndTime.UTC()
	if !rq.EndTime.After(rq.StartTime) {
		return errno.ErrInvalidArgument.WithMessage("endTime must be after startTime")
	}
	if rq.EndTime.Sub(rq.StartTime) > maxWindow {
		return errno.ErrInvalidArgument.WithMessage("an override window must be at most 366 days")
	}
	return nil
}

// ValidateDeleteOncallOverrideRequest checks identity.
func (v *Validator) ValidateDeleteOncallOverrideRequest(_ context.Context, rq *api.DeleteOncallOverrideRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	if strings.TrimSpace(rq.OverrideID) == "" {
		return errno.ErrInvalidArgument.WithMessage("override id is required")
	}
	return nil
}

// ValidateListOncallUnavailabilitiesRequest checks the group name.
func (v *Validator) ValidateListOncallUnavailabilitiesRequest(_ context.Context, rq *api.ListOncallUnavailabilitiesRequest) error {
	return requireGroup(rq.Group)
}

// ValidateCreateOncallUnavailabilityRequest validates inclusive dates.
func (v *Validator) ValidateCreateOncallUnavailabilityRequest(_ context.Context, rq *api.CreateOncallUnavailabilityRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	rq.UserName = strings.TrimSpace(rq.UserName)
	if rq.UserName == "" {
		return errno.ErrInvalidArgument.WithMessage("userName is required")
	}
	start, err := time.Parse("2006-01-02", rq.StartDate)
	if err != nil {
		return errno.ErrInvalidArgument.WithMessage("startDate must be YYYY-MM-DD, got %q", rq.StartDate)
	}
	end, err := time.Parse("2006-01-02", rq.EndDate)
	if err != nil {
		return errno.ErrInvalidArgument.WithMessage("endDate must be YYYY-MM-DD, got %q", rq.EndDate)
	}
	if end.Before(start) {
		return errno.ErrInvalidArgument.WithMessage("endDate must not be before startDate")
	}
	return nil
}

// ValidateDeleteOncallUnavailabilityRequest checks identity.
func (v *Validator) ValidateDeleteOncallUnavailabilityRequest(_ context.Context, rq *api.DeleteOncallUnavailabilityRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	if strings.TrimSpace(rq.UnavailabilityID) == "" {
		return errno.ErrInvalidArgument.WithMessage("unavailability id is required")
	}
	return nil
}

// ValidateListOncallHolidaysRequest checks the group name.
func (v *Validator) ValidateListOncallHolidaysRequest(_ context.Context, rq *api.ListOncallHolidaysRequest) error {
	return requireGroup(rq.Group)
}

// ValidateCreateOncallHolidayRequest validates a holiday date.
func (v *Validator) ValidateCreateOncallHolidayRequest(_ context.Context, rq *api.CreateOncallHolidayRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	rq.Name = strings.TrimSpace(rq.Name)
	if rq.Name == "" {
		return errno.ErrInvalidArgument.WithMessage("name is required")
	}
	if _, err := time.Parse("2006-01-02", rq.Date); err != nil {
		return errno.ErrInvalidArgument.WithMessage("date must be YYYY-MM-DD, got %q", rq.Date)
	}
	return nil
}

// ValidateDeleteOncallHolidayRequest checks identity.
func (v *Validator) ValidateDeleteOncallHolidayRequest(_ context.Context, rq *api.DeleteOncallHolidayRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	if strings.TrimSpace(rq.HolidayID) == "" {
		return errno.ErrInvalidArgument.WithMessage("holiday id is required")
	}
	return nil
}

// ValidateGetOncallEscalationRequest checks the group name.
func (v *Validator) ValidateGetOncallEscalationRequest(_ context.Context, rq *api.GetOncallEscalationRequest) error {
	return requireGroup(rq.Group)
}

// ValidateSaveOncallEscalationRequest validates the policy and its tiers.
func (v *Validator) ValidateSaveOncallEscalationRequest(_ context.Context, rq *api.SaveOncallEscalationRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	if len(rq.Tiers) > maxEscalationTiers {
		return errno.ErrInvalidArgument.WithMessage("a policy may have at most %d tiers", maxEscalationTiers)
	}
	if rq.AutoEscalateFromLevel < 0 || rq.AutoEscalateFromLevel > 4 {
		return errno.ErrInvalidArgument.WithMessage("autoEscalateFromLevel must be between 0 and 4")
	}
	for i := range rq.Tiers {
		tier := &rq.Tiers[i]
		if tier.TimeoutSeconds < 1 || tier.TimeoutSeconds > 86400 {
			return errno.ErrInvalidArgument.WithMessage("tier %d timeoutSeconds must be between 1 and 86400", i+1)
		}
		if !model.IsEscalationTargetType(tier.TargetType) {
			return errno.ErrInvalidArgument.WithMessage("tier %d targetType must be one of schedule, users, both", i+1)
		}
		if tier.TargetType == model.EscalationTargetUsers || tier.TargetType == model.EscalationTargetBoth {
			names := normalizeNames(tier.UserNames)
			if len(names) == 0 {
				return errno.ErrInvalidArgument.WithMessage("tier %d needs at least one userName", i+1)
			}
			tier.UserNames = names
		}
		tier.GroupName = strings.TrimSpace(tier.GroupName)
	}
	return nil
}

// ValidateEscalateRequest checks identity.
func (v *Validator) ValidateEscalateRequest(_ context.Context, rq *api.EscalateRequest) error {
	if err := requireGroup(rq.Group); err != nil {
		return err
	}
	rq.TicketID = strings.TrimSpace(rq.TicketID)
	if rq.TicketID == "" {
		return errno.ErrInvalidArgument.WithMessage("ticketId is required")
	}
	return nil
}

func requireGroup(group string) error {
	if strings.TrimSpace(group) == "" {
		return errno.ErrInvalidArgument.WithMessage("group name is required")
	}
	return nil
}

func validateScheduleDefinition(def *api.OncallScheduleDefinition) error {
	def.Timezone = strings.TrimSpace(def.Timezone)
	if def.Timezone == "" {
		def.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(def.Timezone); err != nil {
		return errno.ErrInvalidArgument.WithMessage("unknown timezone %q", def.Timezone)
	}
	if len(def.Layers) > maxLayers {
		return errno.ErrInvalidArgument.WithMessage("a schedule may have at most %d layers", maxLayers)
	}

	layerNames := make(map[string]struct{}, len(def.Layers))
	totalParticipants := 0
	for i := range def.Layers {
		layer := &def.Layers[i]
		layer.Name = strings.TrimSpace(layer.Name)
		if layer.Name == "" {
			return errno.ErrInvalidArgument.WithMessage("layer %d name is required", i+1)
		}
		if _, dup := layerNames[strings.ToLower(layer.Name)]; dup {
			return errno.ErrInvalidArgument.WithMessage("duplicate layer name %q", layer.Name)
		}
		layerNames[strings.ToLower(layer.Name)] = struct{}{}

		if !model.IsRotationType(layer.RotationType) {
			return errno.ErrInvalidArgument.WithMessage("layer %q rotationType must be one of %v", layer.Name, model.RotationTypes())
		}
		if layer.RotationStart.IsZero() {
			return errno.ErrInvalidArgument.WithMessage("layer %q rotationStart is required", layer.Name)
		}
		if layer.ShiftDurationSeconds <= 0 {
			return errno.ErrInvalidArgument.WithMessage("layer %q shiftDurationSeconds must be positive", layer.Name)
		}
		if len(layer.Participants) == 0 || len(layer.Participants) > maxParticipantsPerLayer {
			return errno.ErrInvalidArgument.WithMessage(
				"layer %q must have between 1 and %d participants", layer.Name, maxParticipantsPerLayer)
		}

		seen := make(map[string]struct{}, len(layer.Participants))
		for j := range layer.Participants {
			participant := &layer.Participants[j]
			participant.UserName = strings.TrimSpace(participant.UserName)
			if participant.UserName == "" {
				return errno.ErrInvalidArgument.WithMessage("layer %q participant %d userName is required", layer.Name, j+1)
			}
			key := strings.ToLower(participant.UserName)
			if _, dup := seen[key]; dup {
				return errno.ErrInvalidArgument.WithMessage("layer %q has duplicate participant %q", layer.Name, participant.UserName)
			}
			seen[key] = struct{}{}
		}
		totalParticipants += len(layer.Participants)
	}
	if totalParticipants > maxParticipants {
		return errno.ErrInvalidArgument.WithMessage("a schedule may have at most %d participants", maxParticipants)
	}
	return nil
}

func parseWindow(rawFrom, rawTo string) (time.Time, time.Time, error) {
	if strings.TrimSpace(rawFrom) == "" || strings.TrimSpace(rawTo) == "" {
		return time.Time{}, time.Time{}, errno.ErrInvalidArgument.WithMessage("from and to are required")
	}
	from, err := time.Parse(time.RFC3339, rawFrom)
	if err != nil {
		return time.Time{}, time.Time{}, errno.ErrInvalidArgument.WithMessage("from must be RFC 3339, got %q", rawFrom)
	}
	to, err := time.Parse(time.RFC3339, rawTo)
	if err != nil {
		return time.Time{}, time.Time{}, errno.ErrInvalidArgument.WithMessage("to must be RFC 3339, got %q", rawTo)
	}
	if err := validateWindow(from, to); err != nil {
		return time.Time{}, time.Time{}, err
	}
	return from.UTC(), to.UTC(), nil
}

func validateWindow(from, to time.Time) error {
	if !to.After(from) {
		return errno.ErrInvalidArgument.WithMessage("to must be after from")
	}
	if to.Sub(from) > maxWindow {
		return errno.ErrInvalidArgument.WithMessage("the requested window must be at most 366 days")
	}
	return nil
}

func normalizeNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	return out
}
