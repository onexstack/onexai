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
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// GetOncallGroupRequest names one on-call group.
type GetOncallGroupRequest = api.GetOncallGroupRequest

// DeleteOncallGroupRequest names one on-call group.
type DeleteOncallGroupRequest = api.DeleteOncallGroupRequest

// ValidateCreateOncallGroupRequest validates a create request.
func (v *Validator) ValidateCreateOncallGroupRequest(ctx context.Context, rq *model.OncallGroupM) error {
	return v.ValidateOncallGroup(ctx, rq)
}

// ValidateUpdateOncallGroupRequest validates an update request. The path is
// authoritative for the name: the binder copies it into the object before this
// runs.
func (v *Validator) ValidateUpdateOncallGroupRequest(ctx context.Context, rq *model.OncallGroupM) error {
	return v.ValidateOncallGroup(ctx, rq)
}

// ValidateGetOncallGroupRequest validates a get request.
func (v *Validator) ValidateGetOncallGroupRequest(_ context.Context, rq *GetOncallGroupRequest) error {
	return requireName(rq.Name)
}

// ValidateDeleteOncallGroupRequest validates a delete request.
func (v *Validator) ValidateDeleteOncallGroupRequest(_ context.Context, rq *DeleteOncallGroupRequest) error {
	return requireName(rq.Name)
}

func requireName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errno.ErrInvalidArgument.WithMessage("name is required")
	}

	return nil
}
