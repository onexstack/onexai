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

// CreateMCP handles POST /v1/agent/mcpservers.
func (h *Handler) CreateMCP(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.MCPV1().Create, h.val.ValidateCreateMCPRequest)
}

// GetMCP handles GET /v1/agent/mcpservers/:name.
func (h *Handler) GetMCP(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.MCPV1().Get, h.val.ValidateGetMCPRequest)
}

// ListMCP handles GET /v1/agent/mcpservers.
func (h *Handler) ListMCP(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.MCPV1().List, h.val.ValidateListMCPRequest)
}

// UpdateMCP handles PUT /v1/agent/mcpservers/:name.
func (h *Handler) UpdateMCP(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateMCPRequest) {
		if rq.Mcp != nil {
			rq.Mcp.Name = c.Param("name")
		}
	}), h.biz.MCPV1().Update, h.val.ValidateUpdateMCPRequest)
}

// DeleteMCP handles DELETE /v1/agent/mcpservers/:name.
func (h *Handler) DeleteMCP(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.MCPV1().Delete, h.val.ValidateDeleteMCPRequest)
}

// TestMCP handles POST /v1/agent/mcpservers/test: one handshake and tool
// listing against the server described in the request, without persisting
// anything.
//
// The response is 200 with ok=false for a failed test: the request was valid,
// the endpoint was not. That is a different answer from a bind or storage
// failure, and the caller can show it without treating the API as broken.
func (h *Handler) TestMCP(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.MCPV1().Test, h.val.ValidateTestMCPRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		mcps := engine.Group("/agent/mcpservers")
		mcps.POST("", h.CreateMCP)
		mcps.GET("", h.ListMCP)
		// Register the static route before the parameterised one; both are
		// distinct methods from the GET below, so gin's tree is unambiguous.
		mcps.POST("/test", h.TestMCP)
		mcps.GET("/:name", h.GetMCP)
		mcps.PUT("/:name", h.UpdateMCP)
		mcps.DELETE("/:name", h.DeleteMCP)
	})
}
