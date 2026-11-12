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

// CreateAgentTemplate handles POST /v1/agent/templates.
func (h *Handler) CreateAgentTemplate(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.AgentTemplateV1().Create, h.val.ValidateCreateAgentTemplateRequest)
}

// GetAgentTemplate handles GET /v1/agent/templates/:name.
func (h *Handler) GetAgentTemplate(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AgentTemplateV1().Get, h.val.ValidateGetAgentTemplateRequest)
}

// ListAgentTemplate handles GET /v1/agent/templates.
func (h *Handler) ListAgentTemplate(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.AgentTemplateV1().List, h.val.ValidateListAgentTemplateRequest)
}

// UpdateAgentTemplate handles PUT /v1/agent/templates/:name.
func (h *Handler) UpdateAgentTemplate(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateAgentTemplateRequest) {
		if rq.GetAgentTemplate() != nil {
			rq.AgentTemplate.Name = c.Param("name") //nolint:protogetter // writing the request field
		}
	}), h.biz.AgentTemplateV1().Update, h.val.ValidateUpdateAgentTemplateRequest)
}

// DeleteAgentTemplate handles DELETE /v1/agent/templates/:name.
func (h *Handler) DeleteAgentTemplate(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AgentTemplateV1().Delete, h.val.ValidateDeleteAgentTemplateRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		templates := engine.Group("/agent/templates")
		templates.POST("", h.CreateAgentTemplate)
		templates.GET("", h.ListAgentTemplate)
		templates.GET("/:name", h.GetAgentTemplate)
		templates.PUT("/:name", h.UpdateAgentTemplate)
		templates.DELETE("/:name", h.DeleteAgentTemplate)
	})
}
