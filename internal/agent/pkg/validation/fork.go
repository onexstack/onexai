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

// ValidateForkSessionRequest checks a session-fork request.
func (v *Validator) ValidateForkSessionRequest(_ context.Context, rq *v1.ForkSessionRequest) error {
	if strings.TrimSpace(rq.GetId()) == "" {
		return errno.ErrInvalidArgument.WithMessage("session id is required")
	}

	if rq.GetUpToSeq() < 0 {
		return errno.ErrInvalidArgument.WithMessage("upToSeq must be a non-negative integer")
	}

	rq.Name = strings.TrimSpace(rq.GetName()) //nolint:protogetter // writing the request field

	return nil
}
