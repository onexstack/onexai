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

// CreateRuntimeHosting handles POST /v1/agent/runtimehostings.
func (h *Handler) CreateRuntimeHosting(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.RuntimeHostingV1().Create, h.val.ValidateCreateRuntimeHostingRequest)
}

// GetRuntimeHosting handles GET /v1/agent/runtimehostings/:name.
func (h *Handler) GetRuntimeHosting(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.RuntimeHostingV1().Get, h.val.ValidateGetRuntimeHostingRequest)
}

// ListRuntimeHosting handles GET /v1/agent/runtimehostings.
func (h *Handler) ListRuntimeHosting(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.RuntimeHostingV1().List, h.val.ValidateListRuntimeHostingRequest)
}

// UpdateRuntimeHosting handles PUT /v1/agent/runtimehostings/:name.
func (h *Handler) UpdateRuntimeHosting(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateRuntimeHostingRequest) {
		if rq.RuntimeHosting != nil {
			rq.RuntimeHosting.Name = c.Param("name")
		}
	}), h.biz.RuntimeHostingV1().Update, h.val.ValidateUpdateRuntimeHostingRequest)
}

// DeleteRuntimeHosting handles DELETE /v1/agent/runtimehostings/:name.
func (h *Handler) DeleteRuntimeHosting(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.RuntimeHostingV1().Delete, h.val.ValidateDeleteRuntimeHostingRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		hostings := engine.Group("/agent/runtimehostings")
		hostings.POST("", h.CreateRuntimeHosting)
		hostings.GET("", h.ListRuntimeHosting)
		hostings.GET("/:name", h.GetRuntimeHosting)
		hostings.PUT("/:name", h.UpdateRuntimeHosting)
		hostings.DELETE("/:name", h.DeleteRuntimeHosting)
	})
}
