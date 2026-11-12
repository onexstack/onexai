// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// CreateAgentBinding handles POST /v1/agent/agentbindings.
func (h *Handler) CreateAgentBinding(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.AgentBindingV1().Create, h.val.ValidateCreateAgentBindingRequest)
}

// GetAgentBinding handles GET /v1/agent/agentbindings/:name.
func (h *Handler) GetAgentBinding(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AgentBindingV1().Get, h.val.ValidateGetAgentBindingRequest)
}

// ListAgentBinding handles GET /v1/agent/agentbindings.
func (h *Handler) ListAgentBinding(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.AgentBindingV1().List, h.val.ValidateListAgentBindingRequest)
}

// UpdateAgentBinding handles PUT /v1/agent/agentbindings/:name.
func (h *Handler) UpdateAgentBinding(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateAgentBindingRequest) {
		if rq.GetAgentBinding() != nil {
			rq.AgentBinding.Name = c.Param("name") //nolint:protogetter // writing the request field
		}
	}), h.biz.AgentBindingV1().Update, h.val.ValidateUpdateAgentBindingRequest)
}

// DeleteAgentBinding handles DELETE /v1/agent/agentbindings/:name.
func (h *Handler) DeleteAgentBinding(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AgentBindingV1().Delete, h.val.ValidateDeleteAgentBindingRequest)
}

// GetEffectiveAgent handles GET /v1/agent/agents/:name/effective.
//
// The effective view answers "what does this agent actually have, and where did
// each item come from". It reads status.resolved, the same set the runtime
// consumes, so the answer cannot drift from execution.
func (h *Handler) GetEffectiveAgent(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AgentBindingV1().GetEffective, h.val.ValidateGetAgentRequest)
}

// PreviewAgentBinding handles POST /v1/agent/agentbindings/preview.
//
// A dry run for a binding that does not exist yet, or for an edit to one.
func (h *Handler) PreviewAgentBinding(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.AgentBindingV1().Preview, h.val.ValidatePreviewAgentBindingRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		// The effective view answers "what does this agent actually have, and
		// where did each item come from". It reads status.resolved, the same set
		// the runtime consumes, so the answer cannot drift from execution.
		engine.GET("/agent/agents/:name/effective", h.GetEffectiveAgent)

		bindings := engine.Group("/agent/agentbindings")
		bindings.POST("", h.CreateAgentBinding)
		bindings.GET("", h.ListAgentBinding)
		bindings.GET("/:name", h.GetAgentBinding)
		bindings.PUT("/:name", h.UpdateAgentBinding)
		bindings.DELETE("/:name", h.DeleteAgentBinding)

		// A dry run for a binding that does not exist yet, or for an edit to one.
		engine.POST("/agent/agentbindings/preview", h.PreviewAgentBinding)
	})
}
