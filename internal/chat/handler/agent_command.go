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

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
)

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		// Granting commands is administering what the assistant can do, so the
		// read-only report carries the same admin gate the write surface used to.
		group := engine.Group("",
			h.AuthMiddleware(),
			bearer.RequireAdmin(),
		)

		// Grants are written as AgentBinding objects in the agent API group; the
		// chat service only reports what they add up to.
		group.GET("/agent-command-coverage", h.GetAgentCommandCoverage)
	})
}

// GetAgentCommandCoverage handles GET /v1/agent-command-coverage.
//
// It answers the question the object listing cannot: what does an agent end up
// with? Commands are deny-by-default, so an agent no binding covers is invisible
// until somebody tries to use one.
//
// The report is not filtered or paginated: an operator reads it to find the
// agent that is missing from it, and a list that has been narrowed is exactly
// the list that hides the answer. `withNothing` is the headline, and the entries
// are every agent the service knows.
func (h *Handler) GetAgentCommandCoverage(c *gin.Context) {
	coverage, err := h.biz.AgentCommandV1().Coverage(c.Request.Context())
	core.WriteResponse(c, coverage, err)
}
