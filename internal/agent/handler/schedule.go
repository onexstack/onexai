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

// ListScheduledExecution handles GET /v1/agent/scheduledruns/:name/executions.
func (h *Handler) ListScheduledExecution(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[v1.ListScheduledExecutionQuery](c),
		h.biz.ScheduledExecutionV1().List, h.val.ValidateListScheduledExecutionRequest)
}

// TriggerScheduledRun handles POST /v1/agent/scheduledruns/:name/trigger.
func (h *Handler) TriggerScheduledRun(c *gin.Context) {
	var req v1.TriggerScheduledRunRequest
	if err := core.ReadRequest(c, &req, c.ShouldBindUri, h.val.ValidateTriggerScheduledRunRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	execution, err := h.biz.ScheduledExecutionV1().Trigger(c.Request.Context(), &req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.JSON(http.StatusAccepted, execution)
}

// CancelScheduledExecution handles POST /v1/agent/scheduledruns/:name/executions/:id/cancel.
func (h *Handler) CancelScheduledExecution(c *gin.Context) {
	var req v1.CancelScheduledExecutionRequest
	if err := core.ReadRequest(c, &req, c.ShouldBindUri, h.val.ValidateCancelScheduledExecutionRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if err := h.biz.ScheduledExecutionV1().Cancel(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func init() { //nolint:gochecknoinits
	RegisterUnified(func(v1 *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		v1.GET("/agent/scheduledruns/:name/executions", h.ListScheduledExecution)
		v1.POST("/agent/scheduledruns/:name/trigger", h.TriggerScheduledRun)
		v1.POST("/agent/scheduledruns/:name/executions/:id/cancel", h.CancelScheduledExecution)
	})
}
