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

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ListRunEventRequest is the run-event query.
type ListRunEventRequest = v1.ListRunEventQuery

// ValidateGetRunRequest validates the run identifier.
func (v *Validator) ValidateGetRunRequest(_ context.Context, rq *v1.GetRunRequest) error {
	return requireRunID(rq.GetId())
}

// ValidateListRunEventRequest validates the run identifier and cursor.
func (v *Validator) ValidateListRunEventRequest(_ context.Context, rq *ListRunEventRequest) error {
	if err := requireRunID(rq.ID); err != nil {
		return err
	}

	if rq.After < 0 {
		return errno.ErrInvalidArgument.WithMessage("after must be a non-negative integer")
	}

	return nil
}

// ValidateCancelRunRequest validates the run identifier.
func (v *Validator) ValidateCancelRunRequest(_ context.Context, rq *v1.CancelRunRequest) error {
	return requireRunID(rq.GetId())
}

func requireRunID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errno.ErrInvalidArgument.WithMessage("run id is required")
	}

	return nil
}

// adminRunStates is the vocabulary the management list accepts. It is the
// admission state, not the run's raw state.
var adminRunStates = map[string]bool{
	"pending": true, "running": true, "succeeded": true, "failed": true, "canceled": true,
}

// adminRunTriggers is the trigger vocabulary the management list accepts.
var adminRunTriggers = map[string]bool{
	"scheduled": true, "manual": true, "retry": true, "unknown": true,
}

// ValidateListAdminRunQuery validates the async-task list page.
func (v *Validator) ValidateListAdminRunQuery(_ context.Context, rq *v1.ListAdminRunQuery) error {
	if rq.Offset < 0 {
		return errno.ErrInvalidArgument.WithMessage("offset must be a non-negative integer")
	}
	if rq.Limit < 0 || rq.Limit > 200 {
		return errno.ErrInvalidArgument.WithMessage("limit must be between 0 and 200")
	}
	if rq.State != "" && !adminRunStates[rq.State] {
		return errno.ErrInvalidArgument.WithMessage("unknown state %q", rq.State)
	}
	if rq.Trigger != "" && !adminRunTriggers[rq.Trigger] {
		return errno.ErrInvalidArgument.WithMessage("unknown trigger %q", rq.Trigger)
	}
	if rq.From < 0 || rq.To < 0 {
		return errno.ErrInvalidArgument.WithMessage("from and to must be non-negative milliseconds")
	}
	// Both bounds are optional, but they are meaningless on their own: a lone
	// bound would silently produce a half-open window with a zero edge.
	if (rq.From == 0) != (rq.To == 0) {
		return errno.ErrInvalidArgument.WithMessage("from and to must be supplied together")
	}
	if rq.To > 0 {
		if rq.To <= rq.From {
			return errno.ErrInvalidArgument.WithMessage("to must be after from")
		}
		if rq.To-rq.From > v1.MonitorMaxWindowMs {
			return errno.ErrInvalidArgument.WithMessage("the window must not exceed seven days")
		}
	}
	return nil
}

// ValidateListAdminRunMonitorQuery validates a monitoring window.
//
// The query has already been through Default(), so a zero From/To/Step/Limit
// means the caller omitted it rather than asked for zero. Negative values are
// still rejected here: Default only fills zeros.
func (v *Validator) ValidateListAdminRunMonitorQuery(
	_ context.Context, rq *v1.ListAdminRunMonitorQuery,
) error {
	agent := strings.TrimSpace(rq.Agent)
	switch {
	case rq.All && agent != "":
		return errno.ErrInvalidArgument.WithMessage("agent and all are mutually exclusive")
	case !rq.All && agent == "":
		// Spelling the fleet mode out keeps an omitted agent from turning a
		// per-agent query into an unbounded scan of every agent's history.
		return errno.ErrInvalidArgument.WithMessage("agent is required unless all=true")
	}
	if rq.From < 0 || rq.To < 0 {
		return errno.ErrInvalidArgument.WithMessage("from and to must be non-negative milliseconds")
	}
	if rq.To <= rq.From {
		return errno.ErrInvalidArgument.WithMessage("to must be after from")
	}

	span := rq.To - rq.From
	if span > v1.MonitorMaxWindowMs {
		return errno.ErrInvalidArgument.WithMessage("the window must not exceed seven days")
	}

	// Default() derives a step from the span when the caller omits one, but it
	// must not silently rewrite a step the caller supplied and got wrong.
	if rq.Step != 0 && !v1.IsMonitorStep(rq.Step) {
		return errno.ErrInvalidArgument.WithMessage("unsupported step %d", rq.Step)
	}
	step := v1.ResolveMonitorStep(rq.Step, span)
	if buckets := (span + step - 1) / step; buckets > v1.MonitorMaxBuckets {
		return errno.ErrInvalidArgument.WithMessage(
			"the window is too long for the requested step (%d buckets, at most %d)",
			buckets, v1.MonitorMaxBuckets,
		)
	}
	if rq.Limit < 0 || rq.Limit > v1.MonitorMaxLimit {
		return errno.ErrInvalidArgument.WithMessage(
			"limit must be between 0 and %d", v1.MonitorMaxLimit,
		)
	}
	return nil
}

// ValidateListAdminRunStuckQuery validates the stuck-run query. The agent is
// optional (empty means the whole fleet); only the limit is bounded here.
func (v *Validator) ValidateListAdminRunStuckQuery(
	_ context.Context, rq *v1.ListAdminRunStuckQuery,
) error {
	if rq.Limit < 0 || rq.Limit > v1.StuckMaxLimit {
		return errno.ErrInvalidArgument.WithMessage(
			"limit must be between 0 and %d", v1.StuckMaxLimit,
		)
	}
	return nil
}

// ValidateGetAdminRunRequest validates one async-task lookup.
func (v *Validator) ValidateGetAdminRunRequest(_ context.Context, rq *v1.GetAdminRunRequest) error {
	return requireRunID(rq.ID)
}

// ValidateListAdminRunEventRequest validates the event page.
func (v *Validator) ValidateListAdminRunEventRequest(_ context.Context, rq *v1.ListAdminRunEventQuery) error {
	if err := requireRunID(rq.ID); err != nil {
		return err
	}
	if rq.After < 0 {
		return errno.ErrInvalidArgument.WithMessage("after must be a non-negative integer")
	}
	if rq.Limit < 0 || rq.Limit > 500 {
		return errno.ErrInvalidArgument.WithMessage("limit must be between 0 and 500")
	}
	return nil
}

// ValidateCancelAdminRunRequest validates a cancellation.
func (v *Validator) ValidateCancelAdminRunRequest(_ context.Context, rq *v1.CancelAdminRunRequest) error {
	return requireRunID(rq.ID)
}

// ValidateRetryAdminRunRequest validates a replay.
func (v *Validator) ValidateRetryAdminRunRequest(_ context.Context, rq *v1.RetryAdminRunRequest) error {
	return requireRunID(rq.ID)
}
