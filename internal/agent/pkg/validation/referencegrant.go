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

	genericvalidation "github.com/onexstack/onexstack/pkg/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agent "github.com/onexstack/onexai/pkg/apis/agent"
	agentvalidation "github.com/onexstack/onexai/pkg/apis/agent/validation"
)

// ValidateReferenceGrantRules returns the rules for a grant payload.
func (v *Validator) ValidateReferenceGrantRules() genericvalidation.Rules { return nameRules }

// ValidateCreateReferenceGrantRequest validates a create request.
func (v *Validator) ValidateCreateReferenceGrantRequest(
	_ context.Context, rq *v1.CreateReferenceGrantRequest,
) error {
	if err := validatePayload(rq.GetReferenceGrant(), "referenceGrant", v.ValidateReferenceGrantRules()); err != nil {
		return err
	}
	return validateReferenceGrant(rq.GetReferenceGrant())
}

// ValidateUpdateReferenceGrantRequest validates an update request.
func (v *Validator) ValidateUpdateReferenceGrantRequest(
	_ context.Context, rq *v1.UpdateReferenceGrantRequest,
) error {
	if err := validatePayload(rq.GetReferenceGrant(), "referenceGrant", v.ValidateReferenceGrantRules()); err != nil {
		return err
	}
	return validateReferenceGrant(rq.GetReferenceGrant())
}

// ValidateGetReferenceGrantRequest validates a get request.
func (v *Validator) ValidateGetReferenceGrantRequest(
	_ context.Context, rq *v1.GetReferenceGrantRequest,
) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateReferenceGrantRules())
}

// ValidateDeleteReferenceGrantRequest validates a delete request.
func (v *Validator) ValidateDeleteReferenceGrantRequest(
	_ context.Context, rq *v1.DeleteReferenceGrantRequest,
) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateReferenceGrantRules())
}

// ValidateListReferenceGrantRequest validates a list request.
func (v *Validator) ValidateListReferenceGrantRequest(
	_ context.Context, rq *v1.ListReferenceGrantRequest,
) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}

func validateReferenceGrant(p *v1.ReferenceGrant) error {
	spec := &agent.ReferenceGrantSpec{
		From: make([]agent.ReferenceGrantFrom, 0, len(p.GetFrom())),
		To:   make([]agent.ReferenceGrantTo, 0, len(p.GetTo())),
	}
	for _, from := range p.GetFrom() {
		spec.From = append(spec.From, agent.ReferenceGrantFrom{
			Group:     from.GetGroup(),
			Kind:      from.GetKind(),
			Namespace: from.GetNamespace(),
		})
	}
	for _, to := range p.GetTo() {
		spec.To = append(spec.To, agent.ReferenceGrantTo{
			Group: to.GetGroup(),
			Kind:  to.GetKind(),
			Name:  to.GetName(),
		})
	}
	if errs := agentvalidation.ValidateReferenceGrant(spec, field.NewPath("spec")); len(errs) > 0 {
		return errno.ErrInvalidArgument.WithMessage("spec is invalid: %s", errs.ToAggregate().Error())
	}
	return nil
}
