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

// CreateReferenceGrant handles POST /v1/agent/referencegrants.
func (h *Handler) CreateReferenceGrant(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.ReferenceGrantV1().Create, h.val.ValidateCreateReferenceGrantRequest)
}

// GetReferenceGrant handles GET /v1/agent/referencegrants/:name.
func (h *Handler) GetReferenceGrant(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ReferenceGrantV1().Get, h.val.ValidateGetReferenceGrantRequest)
}

// ListReferenceGrant handles GET /v1/agent/referencegrants.
func (h *Handler) ListReferenceGrant(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.ReferenceGrantV1().List, h.val.ValidateListReferenceGrantRequest)
}

// UpdateReferenceGrant handles PUT /v1/agent/referencegrants/:name.
func (h *Handler) UpdateReferenceGrant(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateReferenceGrantRequest) {
		if rq.GetReferenceGrant() != nil {
			rq.ReferenceGrant.Name = c.Param("name") //nolint:protogetter // writing the request field
		}
	}), h.biz.ReferenceGrantV1().Update, h.val.ValidateUpdateReferenceGrantRequest)
}

// DeleteReferenceGrant handles DELETE /v1/agent/referencegrants/:name.
func (h *Handler) DeleteReferenceGrant(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ReferenceGrantV1().Delete, h.val.ValidateDeleteReferenceGrantRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		grants := engine.Group("/agent/referencegrants")
		grants.POST("", h.CreateReferenceGrant)
		grants.GET("", h.ListReferenceGrant)
		grants.GET("/:name", h.GetReferenceGrant)
		grants.PUT("/:name", h.UpdateReferenceGrant)
		grants.DELETE("/:name", h.DeleteReferenceGrant)
	})
}
