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

// CreatePrompt handles POST /v1/agent/prompts.
func (h *Handler) CreatePrompt(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.PromptV1().Create, h.val.ValidateCreatePromptRequest)
}

// GetPrompt handles GET /v1/agent/prompts/:name.
func (h *Handler) GetPrompt(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.PromptV1().Get, h.val.ValidateGetPromptRequest)
}

// ListPrompt handles GET /v1/agent/prompts.
func (h *Handler) ListPrompt(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.PromptV1().List, h.val.ValidateListPromptRequest)
}

// UpdatePrompt handles PUT /v1/agent/prompts/:name.
func (h *Handler) UpdatePrompt(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdatePromptRequest) {
		if rq.Prompt != nil {
			rq.Prompt.Name = c.Param("name")
		}
	}), h.biz.PromptV1().Update, h.val.ValidateUpdatePromptRequest)
}

// DeletePrompt handles DELETE /v1/agent/prompts/:name.
func (h *Handler) DeletePrompt(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.PromptV1().Delete, h.val.ValidateDeletePromptRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		prompts := engine.Group("/agent/prompts")
		prompts.POST("", h.CreatePrompt)
		prompts.GET("", h.ListPrompt)
		prompts.GET("/:name", h.GetPrompt)
		prompts.PUT("/:name", h.UpdatePrompt)
		prompts.DELETE("/:name", h.DeletePrompt)
	})
}
