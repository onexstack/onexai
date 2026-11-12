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

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// ExecNamedCommandRequest names the command to execute.
type ExecNamedCommandRequest struct {
	CmdName string `uri:"cmdname"`
}

// GetCommandRequest names the command to read and the namespace it lives in.
type GetCommandRequest = api.GetCommandRequest

// ValidateExecCommandRequest validates the free-form command text.
func (v *Validator) ValidateExecCommandRequest(_ context.Context, rq *v1.ExecCommandRequest) error {
	if strings.TrimSpace(rq.GetCommand()) == "" {
		return errno.ErrInvalidArgument.WithMessage("command is required")
	}

	return nil
}

// ValidateExecNamedCommandRequest validates the command identifier.
func (v *Validator) ValidateExecNamedCommandRequest(_ context.Context, rq *ExecNamedCommandRequest) error {
	if strings.TrimSpace(rq.CmdName) == "" {
		return errno.ErrInvalidArgument.WithMessage("cmdname is required")
	}

	return nil
}

// ValidateListCommandRequest validates the catalog page.
func (v *Validator) ValidateListCommandRequest(_ context.Context, rq *v1.ListCommandRequest) error {
	return validatePage(rq.GetOffset(), rq.GetLimit())
}

// ValidateGetCommandRequest validates the command identifier.
func (v *Validator) ValidateGetCommandRequest(_ context.Context, rq *GetCommandRequest) error {
	if strings.TrimSpace(rq.Name) == "" {
		return errno.ErrInvalidArgument.WithMessage("name is required")
	}

	return nil
}

// validatePage rejects a negative list window. Zero keeps its existing meaning:
// the endpoint returns everything.
func validatePage(offset, limit int32) error {
	if offset < 0 {
		return errno.ErrInvalidArgument.WithMessage("offset must be a non-negative integer")
	}

	if limit < 0 {
		return errno.ErrInvalidArgument.WithMessage("limit must be a non-negative integer")
	}

	return nil
}
