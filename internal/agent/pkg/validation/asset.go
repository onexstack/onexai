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

// ValidateCreateAssetRequest checks an upload body. Kind and Source are filled
// by the generated Default(); the bytes' base64 decode and media-type checks
// stay in the asset business layer, which owns the storage contract.
func (v *Validator) ValidateCreateAssetRequest(_ context.Context, rq *v1.CreateAssetRequest) error {
	if strings.TrimSpace(rq.GetData()) == "" {
		return errno.ErrInvalidArgument.WithMessage("data is required")
	}

	return nil
}
