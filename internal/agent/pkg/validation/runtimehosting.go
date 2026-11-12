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

	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agent "github.com/onexstack/onexai/pkg/apis/agent"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
)

// ValidateRuntimeHostingRules returns the rules for a runtime-hosting payload.
func (v *Validator) ValidateRuntimeHostingRules() genericvalidation.Rules { return nameRules }

// ValidateCreateRuntimeHostingRequest validates a create request.
func (v *Validator) ValidateCreateRuntimeHostingRequest(_ context.Context, rq *v1.CreateRuntimeHostingRequest) error {
	if err := validatePayload(rq.GetRuntimeHosting(), "runtimeHosting", v.ValidateRuntimeHostingRules()); err != nil {
		return err
	}

	return validateRuntimeHosting(rq.GetRuntimeHosting())
}

// ValidateUpdateRuntimeHostingRequest validates an update request.
func (v *Validator) ValidateUpdateRuntimeHostingRequest(_ context.Context, rq *v1.UpdateRuntimeHostingRequest) error {
	if err := validatePayload(rq.GetRuntimeHosting(), "runtimeHosting", v.ValidateRuntimeHostingRules()); err != nil {
		return err
	}

	return validateRuntimeHosting(rq.GetRuntimeHosting())
}

// ValidateGetRuntimeHostingRequest validates a get request.
func (v *Validator) ValidateGetRuntimeHostingRequest(_ context.Context, rq *v1.GetRuntimeHostingRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateRuntimeHostingRules())
}

// ValidateDeleteRuntimeHostingRequest validates a delete request.
func (v *Validator) ValidateDeleteRuntimeHostingRequest(_ context.Context, rq *v1.DeleteRuntimeHostingRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateRuntimeHostingRules())
}

// ValidateListRuntimeHostingRequest validates a list request.
func (v *Validator) ValidateListRuntimeHostingRequest(_ context.Context, rq *v1.ListRuntimeHostingRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// validateRuntimeHosting mirrors the declarative runtime-hosting rules.
func validateRuntimeHosting(hosting *v1.RuntimeHosting) error {
	if strings.TrimSpace(hosting.GetProfileRef()) == "" {
		return errno.ErrInvalidArgument.WithMessage("profileRef is required: a hosting must name the profile it serves")
	}

	switch hosting.GetMode() {
	case agent.HostingModeDocker:
		if hosting.GetImage() == "" {
			return errno.ErrInvalidArgument.WithMessage("image is required: docker hosting needs a digest-pinned image")
		}

		if !agentvalidation.IsDigestPinnedImage(hosting.GetImage()) {
			return errno.ErrInvalidArgument.WithMessage(
				"image %q must be pinned by @sha256:<64 hex characters>", hosting.GetImage())
		}

	case agent.HostingModeProcess:
		if len(hosting.GetCommand()) == 0 {
			return errno.ErrInvalidArgument.WithMessage("command is required: process hosting needs a command")
		}

	default:
		return errno.ErrInvalidArgument.WithMessage(
			"mode %q is not supported (want %q or %q)", hosting.GetMode(), agent.HostingModeDocker, agent.HostingModeProcess)
	}

	if hosting.GetMaxInstances() < 0 {
		return errno.ErrInvalidArgument.WithMessage("maxInstances must not be negative")
	}

	if hosting.GetIdleTTLNanos() < 0 {
		return errno.ErrInvalidArgument.WithMessage("idleTTLNanos must not be negative")
	}

	return nil
}
