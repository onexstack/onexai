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
		templates := engine.Group("/agent/sandboxtemplates")
		templates.POST("", h.CreateSandboxTemplate)
		templates.GET("", h.ListSandboxTemplate)
		templates.GET("/:name", h.GetSandboxTemplate)
		templates.PUT("/:name", h.UpdateSandboxTemplate)
		templates.DELETE("/:name", h.DeleteSandboxTemplate)
	})
}

// CreateSandboxTemplate handles POST /v1/agent/sandboxtemplates.
func (h *Handler) CreateSandboxTemplate(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.SandboxTemplateV1().Create, h.val.ValidateCreateSandboxTemplateRequest)
}

// GetSandboxTemplate handles GET /v1/agent/sandboxtemplates/:name.
func (h *Handler) GetSandboxTemplate(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.SandboxTemplateV1().Get, h.val.ValidateGetSandboxTemplateRequest)
}

// ListSandboxTemplate handles GET /v1/agent/sandboxtemplates.
func (h *Handler) ListSandboxTemplate(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.SandboxTemplateV1().List, h.val.ValidateListSandboxTemplateRequest)
}

// UpdateSandboxTemplate handles PUT /v1/agent/sandboxtemplates/:name.
func (h *Handler) UpdateSandboxTemplate(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateSandboxTemplateRequest) {
		if rq.SandboxTemplate != nil {
			rq.SandboxTemplate.Name = c.Param("name")
		}
	}), h.biz.SandboxTemplateV1().Update, h.val.ValidateUpdateSandboxTemplateRequest)
}

// DeleteSandboxTemplate handles DELETE /v1/agent/sandboxtemplates/:name.
func (h *Handler) DeleteSandboxTemplate(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.SandboxTemplateV1().Delete, h.val.ValidateDeleteSandboxTemplateRequest)
}
