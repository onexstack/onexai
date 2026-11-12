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

	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		runs := engine.Group("/agent/scheduledruns")
		runs.POST("", h.CreateScheduledRun)
		runs.GET("", h.ListScheduledRun)
		runs.GET("/:name", h.GetScheduledRun)
		runs.PUT("/:name", h.UpdateScheduledRun)
		runs.DELETE("/:name", h.DeleteScheduledRun)
	})
}

// CreateScheduledRun handles POST /v1/agent/scheduledruns.
func (h *Handler) CreateScheduledRun(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.ScheduledRunV1().Create, h.val.ValidateCreateScheduledRunRequest)
}

// GetScheduledRun handles GET /v1/agent/scheduledruns/:name.
func (h *Handler) GetScheduledRun(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ScheduledRunV1().Get, h.val.ValidateGetScheduledRunRequest)
}

// ListScheduledRun handles GET /v1/agent/scheduledruns.
func (h *Handler) ListScheduledRun(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.ScheduledRunV1().List, h.val.ValidateListScheduledRunRequest)
}

// UpdateScheduledRun handles PUT /v1/agent/scheduledruns/:name.
func (h *Handler) UpdateScheduledRun(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateScheduledRunRequest) {
		if rq.ScheduledRun != nil {
			rq.ScheduledRun.Name = c.Param("name")
		}
	}), h.biz.ScheduledRunV1().Update, h.val.ValidateUpdateScheduledRunRequest)
}

// DeleteScheduledRun handles DELETE /v1/agent/scheduledruns/:name.
func (h *Handler) DeleteScheduledRun(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ScheduledRunV1().Delete, h.val.ValidateDeleteScheduledRunRequest)
}
