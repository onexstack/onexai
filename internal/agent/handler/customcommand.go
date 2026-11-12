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

	customcommandbiz "github.com/onexstack/onexai/internal/agent/biz/v1/customcommand"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// CreateCustomCommand handles POST /v1/agent/customcommands.
func (h *Handler) CreateCustomCommand(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.CustomCommandV1().Create, h.val.ValidateCreateCustomCommandRequest)
}

// GetCustomCommand handles GET /v1/agent/customcommands/:name.
func (h *Handler) GetCustomCommand(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.CustomCommandV1().Get, h.val.ValidateGetCustomCommandRequest)
}

// ListCustomCommand handles GET /v1/agent/customcommands.
func (h *Handler) ListCustomCommand(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.CustomCommandV1().List, h.val.ValidateListCustomCommandRequest)
}

// UpdateCustomCommand handles PUT /v1/agent/customcommands/:name.
func (h *Handler) UpdateCustomCommand(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateCustomCommandRequest) {
		if rq.GetCustomCommand() != nil {
			rq.CustomCommand.Name = c.Param("name") //nolint:protogetter // writing the request field
		}
	}), h.biz.CustomCommandV1().Update, h.val.ValidateUpdateCustomCommandRequest)
}

// DeleteCustomCommand handles DELETE /v1/agent/customcommands/:name.
func (h *Handler) DeleteCustomCommand(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.CustomCommandV1().Delete, h.val.ValidateDeleteCustomCommandRequest)
}

// InvokeCustomCommand handles POST /v1/agent/customcommands/:name/invoke.
//
// It is a review-shaped call: the body is the proposal, the response carries the
// decision in status.phase. A deterministic refusal is a 200 with Denied; a
// malformed request is a 400.
func (h *Handler) InvokeCustomCommand(c *gin.Context) {
	var req v1.InvokeCustomCommandRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateInvokeCustomCommandRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	commands, ok := h.customCommandBiz(c)
	if !ok {
		return
	}
	var path v1.GetCustomCommandRequest
	if err := core.ReadRequest(c, &path, c.ShouldBindUri, h.val.ValidateGetCustomCommandRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := commands.Invoke(c.Request.Context(), path.GetName(), &req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, resp, nil)
}

// ApproveCustomCommand handles POST /v1/agent/customcommands/:name/approve.
func (h *Handler) ApproveCustomCommand(c *gin.Context) {
	var req v1.ApproveCustomCommandRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateApproveCustomCommandRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	commands, ok := h.customCommandBiz(c)
	if !ok {
		return
	}
	var path v1.GetCustomCommandRequest
	if err := core.ReadRequest(c, &path, c.ShouldBindUri, h.val.ValidateGetCustomCommandRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := commands.Approve(c.Request.Context(), path.GetName(), &req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, resp, nil)
}

// ListCustomCommandAudit handles GET /v1/custom-command-audits.
func (h *Handler) ListCustomCommandAudit(c *gin.Context) {
	var req v1.ListCustomCommandAuditRequest
	if err := core.ReadRequest(c, &req, c.ShouldBindQuery, h.val.ValidateListCustomCommandAuditRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	commands, ok := h.customCommandBiz(c)
	if !ok {
		return
	}
	total, audits, err := commands.ListAudit(c.Request.Context(), customcommandbiz.AuditQuery{
		Command: req.GetCommand(),
		Agent:   req.GetAgent(),
		UserID:  req.GetUserId(),
		Phase:   req.GetPhase(),
		Offset:  int(req.GetOffset()),
		Limit:   int(req.GetLimit()),
	})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, &v1.ListCustomCommandAuditResponse{Audits: audits, Count: total}, nil)
}

// ListContextCustomCommand handles GET /v1/agent/agents/:name/context-commands.
func (h *Handler) ListContextCustomCommand(c *gin.Context) {
	commands, ok := h.customCommandBiz(c)
	if !ok {
		return
	}
	var req v1.GetAgentRequest
	if err := core.ReadRequest(c, &req, c.ShouldBindUri, h.val.ValidateGetAgentRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	items, err := commands.ContextCommands(c.Request.Context(), req.GetName())
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, &v1.ListContextCustomCommandResponse{CustomCommands: items}, nil)
}

// customCommands returns the custom-command business logic, or writes the
// unconfigured answer and reports false. A partly constructed handler (tests,
// a deployment with the surface disabled) must not panic on a nil interface.
func (h *Handler) customCommandBiz(c *gin.Context) (customcommandbiz.CustomCommandBiz, bool) {
	if h.biz == nil || h.biz.CustomCommandV1() == nil {
		core.WriteResponse(c, nil, errno.ErrNotFound.WithMessage(
			"custom commands are not configured on this deployment"))
		return nil, false
	}
	return h.biz.CustomCommandV1(), true
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		commands := engine.Group("/agent/customcommands")
		commands.POST("", h.CreateCustomCommand)
		commands.GET("", h.ListCustomCommand)
		commands.GET("/:name", h.GetCustomCommand)
		commands.PUT("/:name", h.UpdateCustomCommand)
		commands.DELETE("/:name", h.DeleteCustomCommand)
		commands.POST("/:name/invoke", h.InvokeCustomCommand)
		commands.POST("/:name/approve", h.ApproveCustomCommand)

		engine.GET("/agent/customcommands/audits", h.ListCustomCommandAudit)
		engine.GET("/agent/agents/:name/context-commands", h.ListContextCustomCommand)
	})
}
