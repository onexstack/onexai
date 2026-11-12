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
		profiles := engine.Group("/agent/runtimeprofiles")
		profiles.POST("", h.CreateRuntimeProfile)
		profiles.GET("", h.ListRuntimeProfile)
		profiles.GET("/:name", h.GetRuntimeProfile)
		profiles.PUT("/:name", h.UpdateRuntimeProfile)
		profiles.DELETE("/:name", h.DeleteRuntimeProfile)
	})
}

// CreateRuntimeProfile handles POST /v1/agent/runtimeprofiles.
func (h *Handler) CreateRuntimeProfile(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.RuntimeProfileV1().Create, h.val.ValidateCreateRuntimeProfileRequest)
}

// GetRuntimeProfile handles GET /v1/agent/runtimeprofiles/:name.
func (h *Handler) GetRuntimeProfile(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.RuntimeProfileV1().Get, h.val.ValidateGetRuntimeProfileRequest)
}

// ListRuntimeProfile handles GET /v1/agent/runtimeprofiles.
func (h *Handler) ListRuntimeProfile(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.RuntimeProfileV1().List, h.val.ValidateListRuntimeProfileRequest)
}

// UpdateRuntimeProfile handles PUT /v1/agent/runtimeprofiles/:name.
func (h *Handler) UpdateRuntimeProfile(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateRuntimeProfileRequest) {
		if rq.RuntimeProfile != nil {
			rq.RuntimeProfile.Name = c.Param("name")
		}
	}), h.biz.RuntimeProfileV1().Update, h.val.ValidateUpdateRuntimeProfileRequest)
}

// DeleteRuntimeProfile handles DELETE /v1/agent/runtimeprofiles/:name.
func (h *Handler) DeleteRuntimeProfile(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.RuntimeProfileV1().Delete, h.val.ValidateDeleteRuntimeProfileRequest)
}
