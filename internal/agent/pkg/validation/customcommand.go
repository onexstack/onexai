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

// ValidateInvokeCustomCommandRequest validates a command invocation.
//
// Uid is filled by the generated Default(). Agent is not required: the service
// answers a deterministic Denied for an unknown agent rather than a 400.
func (v *Validator) ValidateInvokeCustomCommandRequest(_ context.Context, rq *v1.InvokeCustomCommandRequest) error {
	return nil
}

// ValidateApproveCustomCommandRequest validates an approval decision.
func (v *Validator) ValidateApproveCustomCommandRequest(_ context.Context, rq *v1.ApproveCustomCommandRequest) error {
	if strings.TrimSpace(rq.GetApprovalId()) == "" {
		return errno.ErrInvalidArgument.WithMessage("approvalId is required")
	}

	return nil
}

// ValidateListCustomCommandAuditRequest validates the audit page.
func (v *Validator) ValidateListCustomCommandAuditRequest(_ context.Context, rq *v1.ListCustomCommandAuditRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, listRules, "Offset", "Limit")
}
