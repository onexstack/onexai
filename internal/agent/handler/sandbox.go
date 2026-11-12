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
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// CreateSandbox handles POST /v1/sandboxes.
func (h *Handler) CreateSandbox(c *gin.Context) {
	var req v1.CreateSandboxRequest
	if err := core.ReadRequest(c, &req, c.ShouldBindJSON, h.val.ValidateCreateSandboxRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	created, err := h.biz.SandboxV1().Create(c.Request.Context(), &req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

// GetSandbox handles GET /v1/sandboxes/:id.
func (h *Handler) GetSandbox(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[v1.GetSandboxRequest](c),
		h.biz.SandboxV1().Get, h.val.ValidateGetSandboxRequest)
}

// DeleteSandbox handles DELETE /v1/sandboxes/:id.
func (h *Handler) DeleteSandbox(c *gin.Context) {
	var req v1.DeleteSandboxRequest
	if err := core.ReadRequest(c, &req, restbind.URIAndQuery[v1.DeleteSandboxRequest](c), h.val.ValidateDeleteSandboxRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if err := h.biz.SandboxV1().Delete(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ExecSandbox handles POST /v1/sandboxes/:id/exec.
func (h *Handler) ExecSandbox(c *gin.Context) {
	core.HandleAllRequest(c, h.biz.SandboxV1().Exec, h.val.ValidateExecSandboxRequest)
}

// ReadSandboxFile handles GET /v1/sandboxes/:id/files.
func (h *Handler) ReadSandboxFile(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[v1.ReadSandboxFileRequest](c),
		h.biz.SandboxV1().ReadFile, h.val.ValidateReadSandboxFileRequest)
}

// WriteSandboxFile handles PUT /v1/sandboxes/:id/files.
func (h *Handler) WriteSandboxFile(c *gin.Context) {
	var req v1.WriteSandboxFileRequest
	if err := core.ShouldBindAll(c, &req, h.val.ValidateWriteSandboxFileRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if err := h.biz.SandboxV1().WriteFile(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func init() { //nolint:gochecknoinits
	Register(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		engine.POST("/sandboxes", h.CreateSandbox)
		engine.GET("/sandboxes/:id", h.GetSandbox)
		engine.DELETE("/sandboxes/:id", h.DeleteSandbox)
		engine.POST("/sandboxes/:id/exec", h.ExecSandbox)
		engine.GET("/sandboxes/:id/files", h.ReadSandboxFile)
		engine.PUT("/sandboxes/:id/files", h.WriteSandboxFile)
	})
}
