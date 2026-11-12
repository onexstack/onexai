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

package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/errno"
)

// agentOrFail reports whether this deployment has an agent engine, writing the
// response when it does not.
//
// It is a server fault rather than a caller error: the caller asked a question
// the deployment is not configured to answer.
func (h *Handler) agentOrFail(c *gin.Context) bool {
	if h.biz.Agent() != nil {
		return true
	}

	core.WriteResponse(c, nil, errno.ErrInternal.WithMessage("AI engine is not configured"))

	return false
}
