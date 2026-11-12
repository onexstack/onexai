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
)

// ValidateSandboxTemplateRules returns the rules for a sandbox-template payload.
func (v *Validator) ValidateSandboxTemplateRules() genericvalidation.Rules { return nameRules }

// ValidateCreateSandboxTemplateRequest validates a create request.
func (v *Validator) ValidateCreateSandboxTemplateRequest(_ context.Context, rq *v1.CreateSandboxTemplateRequest) error {
	if err := validatePayload(rq.GetSandboxTemplate(), "sandboxTemplate", v.ValidateSandboxTemplateRules()); err != nil {
		return err
	}

	return validateSandboxTemplate(rq.GetSandboxTemplate())
}

// ValidateUpdateSandboxTemplateRequest validates an update request.
func (v *Validator) ValidateUpdateSandboxTemplateRequest(_ context.Context, rq *v1.UpdateSandboxTemplateRequest) error {
	if err := validatePayload(rq.GetSandboxTemplate(), "sandboxTemplate", v.ValidateSandboxTemplateRules()); err != nil {
		return err
	}

	return validateSandboxTemplate(rq.GetSandboxTemplate())
}

// ValidateGetSandboxTemplateRequest validates a get request.
func (v *Validator) ValidateGetSandboxTemplateRequest(_ context.Context, rq *v1.GetSandboxTemplateRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateSandboxTemplateRules())
}

// ValidateDeleteSandboxTemplateRequest validates a delete request.
func (v *Validator) ValidateDeleteSandboxTemplateRequest(_ context.Context, rq *v1.DeleteSandboxTemplateRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateSandboxTemplateRules())
}

// ValidateListSandboxTemplateRequest validates a list request.
func (v *Validator) ValidateListSandboxTemplateRequest(_ context.Context, rq *v1.ListSandboxTemplateRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// validateSandboxTemplate mirrors the declarative sandbox-template rules.
func validateSandboxTemplate(template *v1.SandboxTemplate) error {
	if template.GetImage() == "" {
		return errno.ErrInvalidArgument.WithMessage("image is required: a sandbox template must name an image")
	}

	// A tag can move under a template that already ran, which would make "which
	// image did this sandbox use" unanswerable after the fact.
	if !strings.Contains(template.GetImage(), "@sha256:") {
		return errno.ErrInvalidArgument.WithMessage(
			"image %q must be pinned by digest (name@sha256:...)", template.GetImage())
	}

	for _, item := range template.GetEnv() {
		if strings.TrimSpace(item) == "" {
			return errno.ErrInvalidArgument.WithMessage("env: an environment entry must not be empty")
		}
	}

	if template.GetTtlSeconds() < 0 {
		return errno.ErrInvalidArgument.WithMessage("ttlSeconds must not be negative")
	}

	return nil
}
