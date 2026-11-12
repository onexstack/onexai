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

// CreateWebhook handles POST /v1/agent/webhooks.
func (h *Handler) CreateWebhook(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.WebhookV1().Create, h.val.ValidateCreateWebhookRequest)
}

// GetWebhook handles GET /v1/agent/webhooks/:name.
func (h *Handler) GetWebhook(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.WebhookV1().Get, h.val.ValidateGetWebhookRequest)
}

// ListWebhook handles GET /v1/agent/webhooks.
func (h *Handler) ListWebhook(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.WebhookV1().List, h.val.ValidateListWebhookRequest)
}

// UpdateWebhook handles PUT /v1/agent/webhooks/:name. The path names the hook; a name
// in the body is ignored, as it is for every other resource endpoint.
func (h *Handler) UpdateWebhook(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateWebhookRequest) {
		if rq.Webhook != nil {
			rq.Webhook.Name = c.Param("name")
		}
	}), h.biz.WebhookV1().Update, h.val.ValidateUpdateWebhookRequest)
}

// DeleteWebhook handles DELETE /v1/agent/webhooks/:name.
func (h *Handler) DeleteWebhook(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.WebhookV1().Delete, h.val.ValidateDeleteWebhookRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		hooks := engine.Group("/agent/webhooks")
		hooks.POST("", h.CreateWebhook)
		hooks.GET("", h.ListWebhook)
		hooks.GET("/:name", h.GetWebhook)
		hooks.PUT("/:name", h.UpdateWebhook)
		hooks.DELETE("/:name", h.DeleteWebhook)
	})
}
