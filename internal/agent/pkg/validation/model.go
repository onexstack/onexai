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

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ValidateTestModelRequest validates a TestModelRequest.
//
// TestModelRequest.Default sets the inline model and falls back to its name.
// The provider/base URL/model fallbacks need the stored row, so they live in
// the biz Test method, which owns that row.
func (v *Validator) ValidateTestModelRequest(_ context.Context, rq *v1.TestModelRequest) error {
	if rq.GetModel() == nil {
		// Defensive: Default() always installs one, but a caller that bypasses
		// binding should not panic here.
		rq.Model = new(v1.Model)
	}

	return nil
}
