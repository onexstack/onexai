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

// SharedRunRequest names a run reached through a share token.
type SharedRunRequest = v1.SharedRunParams

// SharedRunEventsRequest is the shared run-event query.
type SharedRunEventsRequest = v1.SharedRunEventsQuery

// SharedSessionRunsRequest names a session reached through a share token.
type SharedSessionRunsRequest = v1.SharedSessionRunsParams

// ValidateListSessionShareRequest validates the conversation identifier.
func (v *Validator) ValidateListSessionShareRequest(_ context.Context, rq *v1.ListSessionShareRequest) error {
	return requireSessionID(rq.GetId())
}

// ValidateRevokeSessionShareRequest validates the conversation and grant ids.
func (v *Validator) ValidateRevokeSessionShareRequest(_ context.Context, rq *v1.RevokeSessionShareRequest) error {
	if err := requireSessionID(rq.GetId()); err != nil {
		return err
	}

	if strings.TrimSpace(rq.GetShareId()) == "" {
		return errno.ErrInvalidArgument.WithMessage("share id is required")
	}

	return nil
}

// ValidateSharedRunRequest validates the shared run identifier.
func (v *Validator) ValidateSharedRunRequest(_ context.Context, rq *SharedRunRequest) error {
	return requireSessionID(rq.ID)
}

// ValidateSharedRunEventsRequest validates the shared run identifier and cursor.
func (v *Validator) ValidateSharedRunEventsRequest(_ context.Context, rq *SharedRunEventsRequest) error {
	if err := requireSessionID(rq.ID); err != nil {
		return err
	}

	if rq.After < 0 {
		return errno.ErrInvalidArgument.WithMessage("after must be a non-negative integer")
	}

	return nil
}

// ValidateSharedSessionRunsRequest validates the shared session identifier.
func (v *Validator) ValidateSharedSessionRunsRequest(_ context.Context, rq *SharedSessionRunsRequest) error {
	return requireSessionID(rq.ID)
}

func requireSessionID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errno.ErrInvalidArgument.WithMessage("session id is required")
	}

	return nil
}
