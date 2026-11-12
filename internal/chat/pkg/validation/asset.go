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
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// GetAssetContentRequest names the asset whose bytes are read.
type GetAssetContentRequest struct {
	AssetID string `uri:"assetID"`
}

// ValidateGetAssetContentRequest validates the asset identifier.
func (v *Validator) ValidateGetAssetContentRequest(_ context.Context, rq *GetAssetContentRequest) error {
	if strings.TrimSpace(rq.AssetID) == "" {
		return errno.ErrInvalidArgument.WithMessage("assetID is required")
	}

	return nil
}

// ValidateCreateAssetRequest validates an upload body. Kind and Source are
// filled by the generated Default() of the shared agent request type; the
// caller's identity is stamped by the handler from the token.
func (v *Validator) ValidateCreateAssetRequest(_ context.Context, rq *agentv1.CreateAssetRequest) error {
	if strings.TrimSpace(rq.GetData()) == "" {
		return errno.ErrInvalidArgument.WithMessage("data is required")
	}

	return nil
}
