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
	"github.com/onexstack/onexai/internal/pkg/restbind"
)

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		// The command catalogue is a read-only projection of the compiled
		// command set. There is deliberately no POST/PUT/DELETE: commandreg
		// reconciles the objects, so a write would be undone on the next sync.
		authed := engine.Group("", h.AuthMiddleware())
		authed.GET("/chat/commands", h.ListCommand)
		authed.GET("/chat/commands/:name", h.GetCommand)
	})
}

// ListCommand handles GET /v1/chat/commands.
func (h *Handler) ListCommand(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.CommandV1().List, h.val.ValidateListCommandRequest)
}

// GetCommand handles GET /v1/chat/commands/:name.
func (h *Handler) GetCommand(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[api.GetCommandRequest](c),
		h.biz.CommandV1().Get, h.val.ValidateGetCommandRequest)
}
