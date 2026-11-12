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
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// ForkAgentSession handles POST /v1/agent/sessions/:id/fork.
//
// The fork copies history up to a message sequence, not a live process: the
// new session runs the same agent and revision but has its own identity, so
// the two conversations cannot affect each other.
func (h *Handler) ForkAgentSession(c *gin.Context) {
	var req v1.ForkSessionRequest
	if err := core.ReadRequest(c, &req, restbind.JSONWithPathAllowEmpty(c, func(rq *v1.ForkSessionRequest) {
		rq.Id = c.Param("id") //nolint:protogetter // writing the request field
	}), h.val.ValidateForkSessionRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	forked, err := h.biz.RunV1().Fork(c.Request.Context(), &req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.JSON(http.StatusCreated, forked)
}

func init() { //nolint:gochecknoinits
	Register(func(v1 *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		v1.POST("/agent/sessions/:id/fork", h.ForkAgentSession)
	})
}
