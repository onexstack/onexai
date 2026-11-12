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
	"encoding/json"
	"strings"

	genericvalidation "github.com/onexstack/onexstack/pkg/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agent "github.com/onexstack/onexai/pkg/apis/agent"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
)

// ValidateAgentTemplateRules returns the rules for an agent-template payload.
func (v *Validator) ValidateAgentTemplateRules() genericvalidation.Rules { return nameRules }

// ValidateCreateAgentTemplateRequest validates a create request.
func (v *Validator) ValidateCreateAgentTemplateRequest(_ context.Context, rq *v1.CreateAgentTemplateRequest) error {
	if err := validatePayload(rq.GetAgentTemplate(), "agentTemplate", v.ValidateAgentTemplateRules()); err != nil {
		return err
	}

	return validateAgentTemplateSpec(rq.GetAgentTemplate())
}

// ValidateUpdateAgentTemplateRequest validates an update request.
func (v *Validator) ValidateUpdateAgentTemplateRequest(_ context.Context, rq *v1.UpdateAgentTemplateRequest) error {
	if err := validatePayload(rq.GetAgentTemplate(), "agentTemplate", v.ValidateAgentTemplateRules()); err != nil {
		return err
	}

	return validateAgentTemplateSpec(rq.GetAgentTemplate())
}

// ValidateGetAgentTemplateRequest validates a get request.
func (v *Validator) ValidateGetAgentTemplateRequest(_ context.Context, rq *v1.GetAgentTemplateRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAgentTemplateRules())
}

// ValidateDeleteAgentTemplateRequest validates a delete request.
func (v *Validator) ValidateDeleteAgentTemplateRequest(_ context.Context, rq *v1.DeleteAgentTemplateRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAgentTemplateRules())
}

// ValidateListAgentTemplateRequest validates a list request.
func (v *Validator) ValidateListAgentTemplateRequest(_ context.Context, rq *v1.ListAgentTemplateRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

// validateAgentTemplateSpec decodes the JSON spec and runs the same
// declarative rules the control plane applies. An empty spec stays allowed: it
// is the records written before the field existed.
func validateAgentTemplateSpec(template *v1.AgentTemplate) error {
	raw := template.GetSpec()
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	var versioned agentv1alpha1.AgentTemplateSpec

	if err := json.Unmarshal([]byte(raw), &versioned); err != nil {
		return errno.ErrInvalidArgument.WithMessage("spec is not a valid AgentTemplateSpec: %s", err.Error())
	}

	var internal agent.AgentTemplateSpec

	if err := agentv1alpha1.Convert_v1alpha1_AgentTemplateSpec_To_agent_AgentTemplateSpec(&versioned, &internal, nil); err != nil {
		return errno.ErrInvalidArgument.WithMessage("spec is not a valid AgentTemplateSpec: %s", err.Error())
	}

	if errs := agentvalidation.ValidateAgentTemplate(&internal, field.NewPath("spec")); len(errs) > 0 {
		return errno.ErrInvalidArgument.WithMessage("spec is invalid: %s", errs.ToAggregate().Error())
	}

	// The spec is the source of truth for these two labels; the envelope may
	// omit them. Default() cannot see inside the JSON, so the fill happens here
	// where the spec has already been decoded and validated.
	if template.GetDisplayName() == "" {
		template.DisplayName = versioned.DisplayName //nolint:protogetter // writing the request field
	}

	if template.GetDescription() == "" {
		template.Description = versioned.Description //nolint:protogetter // writing the request field
	}

	return nil
}
