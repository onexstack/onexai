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
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/restbind"
	"github.com/onexstack/onexai/internal/pkg/safego"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func init() { //nolint:gochecknoinits
	RegisterAdmin(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		// The management surface is the browser's view of the asynchronous
		// queue. It is separate from the signed /v1/agent/runs face: the two
		// carry different credentials, and gin binds a route to one chain.
		runs := engine.Group("/agent/runs")
		runs.GET("", h.ListAdminAgentRuns)
		// Static segment siblings of /:id: gin gives them priority, so
		// "monitor" and "stuck" are never read as run ids.
		runs.GET("/monitor", h.MonitorAdminAgentRuns)
		runs.GET("/stuck", h.ListAdminAgentStuckRuns)
		runs.GET("/:id", h.GetAdminAgentRun)
		runs.GET("/:id/events", h.ListAdminAgentRunEvents)
		runs.POST("/:id/cancel", h.CancelAdminAgentRun)
		runs.POST("/:id/retry", h.RetryAdminAgentRun)
	})
}

// ListAdminAgentRuns handles GET /v1/admin/agent/runs.
func (h *Handler) ListAdminAgentRuns(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.RunV1().ListAdminRuns, h.val.ValidateListAdminRunQuery)
}

// MonitorAdminAgentRuns handles GET /v1/admin/agent/runs/monitor.
func (h *Handler) MonitorAdminAgentRuns(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.RunV1().ListAdminRunMonitor, h.val.ValidateListAdminRunMonitorQuery)
}

// ListAdminAgentStuckRuns handles GET /v1/admin/agent/runs/stuck.
func (h *Handler) ListAdminAgentStuckRuns(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.RunV1().ListAdminRunStuck, h.val.ValidateListAdminRunStuckQuery)
}

// GetAdminAgentRun handles GET /v1/admin/agent/runs/:id.
func (h *Handler) GetAdminAgentRun(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.RunV1().GetAdminRun, h.val.ValidateGetAdminRunRequest)
}

// ListAdminAgentRunEvents handles GET /v1/admin/agent/runs/:id/events.
func (h *Handler) ListAdminAgentRunEvents(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[v1.ListAdminRunEventQuery](c),
		h.biz.RunV1().ListAdminRunEvents, h.val.ValidateListAdminRunEventRequest)
}

// CancelAdminAgentRun handles POST /v1/admin/agent/runs/:id/cancel.
func (h *Handler) CancelAdminAgentRun(c *gin.Context) {
	var req v1.CancelAdminRunRequest
	if err := core.ReadRequest(c, &req, c.ShouldBindUri, h.val.ValidateCancelAdminRunRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if err := h.biz.RunV1().CancelAdminRun(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RetryAdminAgentRun handles POST /v1/admin/agent/runs/:id/retry.
//
// The new admission is returned with 202 and executed in the background, the
// same way Admit is turned into work by the signed create endpoint.
func (h *Handler) RetryAdminAgentRun(c *gin.Context) {
	var req v1.RetryAdminRunRequest
	if err := core.ReadRequest(c, &req, c.ShouldBindUri, h.val.ValidateRetryAdminRunRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	admission, err := h.biz.RunV1().RetryAdminRun(c.Request.Context(), &req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	safego.Go(context.Background(), "run retry execute "+admission.ID, func() {
		h.biz.RunV1().Execute(context.Background(), admission.ID)
	})
	c.JSON(http.StatusAccepted, createAgentRunResponse{
		ID: admission.ID, SessionID: admission.SessionID, State: admission.State,
	})
}
