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

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/restbind"
)

// CreateOncallGroup handles POST /v1/oncall-groups.
func (h *Handler) CreateOncallGroup(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.OncallGroupV1().Create, h.val.ValidateCreateOncallGroupRequest)
}

// GetOncallGroup handles GET /v1/oncall-groups/:name.
func (h *Handler) GetOncallGroup(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.OncallGroupV1().Get, h.val.ValidateGetOncallGroupRequest)
}

// ListOncallGroup handles GET /v1/oncall-groups.
func (h *Handler) ListOncallGroup(c *gin.Context) {
	core.HandleRequest(c, c.ShouldBindQuery, h.biz.OncallGroupV1().List, nil)

}

// UpdateOncallGroup handles PUT /v1/oncall-groups/:name.
func (h *Handler) UpdateOncallGroup(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *api.OncallGroup) {
		// The path is authoritative for identity: a body that names a different
		// group must not be able to move the update onto it.
		rq.Name = c.Param("name")
	}), h.biz.OncallGroupV1().Update, h.val.ValidateUpdateOncallGroupRequest)
}

// DeleteOncallGroup handles DELETE /v1/oncall-groups/:name.
func (h *Handler) DeleteOncallGroup(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.OncallGroupV1().Delete, h.val.ValidateDeleteOncallGroupRequest)
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		// The rota is readable by any signed-in operator and writable only by
		// an administrator. This closes the gap ticket.go used to point out:
		// these routes carried no middleware at all.
		groups := engine.Group("/oncall-groups", h.AuthMiddleware())
		writes := groups.Group("", bearer.RequireAdmin())

		writes.POST("", h.CreateOncallGroup)
		groups.GET("", h.ListOncallGroup)
		groups.GET("/:name", h.GetOncallGroup)
		writes.PUT("/:name", h.UpdateOncallGroup)
		writes.DELETE("/:name", h.DeleteOncallGroup)
	})
}
