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

	genericvalidation "github.com/onexstack/onexstack/pkg/validation"
	"github.com/robfig/cron/v3"

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ValidateScheduledRunRules returns the rules for a scheduled-run payload.
func (v *Validator) ValidateScheduledRunRules() genericvalidation.Rules { return nameRules }

// ValidateCreateScheduledRunRequest validates a create request.
func (v *Validator) ValidateCreateScheduledRunRequest(_ context.Context, rq *v1.CreateScheduledRunRequest) error {
	if err := validatePayload(rq.GetScheduledRun(), "scheduledRun", v.ValidateScheduledRunRules()); err != nil {
		return err
	}

	return validateScheduledRun(rq.GetScheduledRun())
}

// ValidateUpdateScheduledRunRequest validates an update request.
func (v *Validator) ValidateUpdateScheduledRunRequest(_ context.Context, rq *v1.UpdateScheduledRunRequest) error {
	if err := validatePayload(rq.GetScheduledRun(), "scheduledRun", v.ValidateScheduledRunRules()); err != nil {
		return err
	}

	return validateScheduledRun(rq.GetScheduledRun())
}

// ValidateGetScheduledRunRequest validates a get request.
func (v *Validator) ValidateGetScheduledRunRequest(_ context.Context, rq *v1.GetScheduledRunRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateScheduledRunRules())
}

// ValidateDeleteScheduledRunRequest validates a delete request.
func (v *Validator) ValidateDeleteScheduledRunRequest(_ context.Context, rq *v1.DeleteScheduledRunRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateScheduledRunRules())
}

// ValidateListScheduledRunRequest validates a list request.
func (v *Validator) ValidateListScheduledRunRequest(_ context.Context, rq *v1.ListScheduledRunRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// ValidateTriggerScheduledRunRequest validates a manual trigger.
func (v *Validator) ValidateTriggerScheduledRunRequest(_ context.Context, rq *v1.TriggerScheduledRunRequest) error {
	return genericvalidation.ValidateAllFields(rq, nameRules)
}

// ValidateCancelScheduledExecutionRequest validates an execution cancellation.
func (v *Validator) ValidateCancelScheduledExecutionRequest(_ context.Context, rq *v1.CancelScheduledExecutionRequest) error {
	if err := genericvalidation.ValidateAllFields(rq, nameRules); err != nil {
		return err
	}

	if strings.TrimSpace(rq.GetId()) == "" {
		return errno.ErrInvalidArgument.WithMessage("execution id is required")
	}

	return nil
}

// validateScheduledRun mirrors the declarative scheduled-run rules, including
// the cron parse: a schedule the scheduler cannot load is a run that never
// fires, and accepting it would hide the typo until the first missed deadline.
func validateScheduledRun(scheduled *v1.ScheduledRun) error {
	if strings.TrimSpace(scheduled.GetAgent()) == "" {
		return errno.ErrInvalidArgument.WithMessage("agent is required: a schedule must name an agent")
	}

	prompt := strings.TrimSpace(scheduled.GetPrompt())
	if prompt == "" {
		return errno.ErrInvalidArgument.WithMessage("prompt is required: a schedule must carry a prompt")
	}

	if len(scheduled.GetPrompt()) > 32*1024 {
		return errno.ErrInvalidArgument.WithMessage("prompt must be at most 32 KiB")
	}

	fields := strings.Fields(scheduled.GetSchedule())
	if len(fields) != 5 {
		return errno.ErrInvalidArgument.WithMessage("schedule %q must be a five-field cron expression", scheduled.GetSchedule())
	}

	tz := scheduled.GetTimeZone()
	if tz == "" {
		tz = "UTC"
	}

	if _, err := time.LoadLocation(tz); err != nil {
		return errno.ErrInvalidArgument.WithMessage("timeZone %q: %s", scheduled.GetTimeZone(), err.Error())
	}

	if _, err := cron.ParseStandard("CRON_TZ=" + tz + " " + strings.Join(fields, " ")); err != nil {
		return errno.ErrInvalidArgument.WithMessage("schedule %q: %s", scheduled.GetSchedule(), err.Error())
	}

	if scheduled.GetExecutionTimeoutNanos() < 0 {
		return errno.ErrInvalidArgument.WithMessage("executionTimeoutNanos must not be negative")
	}

	return nil
}
