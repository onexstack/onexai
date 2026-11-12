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
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/errmsg"
	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// CreateAgent handles POST /v1/agents.
func (h *Handler) CreateAgent(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.AgentV1().Create, h.val.ValidateCreateAgentRequest)
}

// GetAgent handles GET /v1/agent/agents/:name.
func (h *Handler) GetAgent(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AgentV1().Get, h.val.ValidateGetAgentRequest)
}

// ListAgent handles GET /v1/agents.
func (h *Handler) ListAgent(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.AgentV1().List, h.val.ValidateListAgentRequest)
}

// UpdateAgent handles PUT /v1/agent/agents/:name.
func (h *Handler) UpdateAgent(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateAgentRequest) {
		if rq.GetAgent() != nil {
			rq.Agent.Name = c.Param("name")
		}
	}), h.biz.AgentV1().Update, h.val.ValidateUpdateAgentRequest)
}

// DeleteAgent handles DELETE /v1/agent/agents/:name.
func (h *Handler) DeleteAgent(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AgentV1().Delete, h.val.ValidateDeleteAgentRequest)
}

// RegenerateAgentPrompt handles POST /v1/agent/agents/:name/regenerate-prompt.
//
// It removes the derived aggregated Prompt and lets the controller rebuild it
// from the bound sources. Deleting is the same reset whether the aggregate was
// generated or manually overridden.
func (h *Handler) RegenerateAgentPrompt(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AgentV1().RegeneratePrompt, h.val.ValidateGetAgentRequest)
}

// PreviewA2A handles POST /v1/agent/agents/preview: fetch and normalize a card
// described inline, without persisting anything.
func (h *Handler) PreviewA2A(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.AgentV1().PreviewA2A, h.val.ValidatePreviewA2ARequest)
}

// InvokeA2A handles POST /v1/agent/agents/:name/invoke.
//
// Binding follows core.HandleAllRequest: the path is read first and the body
// second, so a body "name" field wins. Callers address the agent by the path.
func (h *Handler) InvokeA2A(c *gin.Context) {
	core.HandleAllRequest(c, h.biz.AgentV1().InvokeA2A, h.val.ValidateInvokeAgentA2ARequest)
}

// InvokeA2AStream handles POST /v1/agent/agents/:name/invoke/stream.
func (h *Handler) InvokeA2AStream(c *gin.Context) {
	rq := new(v1.InvokeAgentA2ARequest)
	if err := core.ShouldBindAll(c, rq, h.val.ValidateInvokeAgentA2ARequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if err := openSSE(c); err != nil {
		_ = err // the stream is already committed; the error is advisory
	}
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	invocationID, events, err := h.biz.AgentV1().StreamA2A(c.Request.Context(), rq)
	if err != nil {
		writeSSE(c, map[string]any{"type": "error", "message": errmsg.Text(err)})
		return
	}
	writeSSE(c, map[string]any{"type": "invocation", "invocationId": invocationID})
	for event := range events {
		writeSSE(c, event)
	}
	writeSSE(c, map[string]any{"type": "done"})
}

// GetA2ATask handles GET /v1/agent/agents/:name/tasks/:taskId.
func (h *Handler) GetA2ATask(c *gin.Context) {
	resp, err := h.biz.AgentV1().GetA2ATask(c.Request.Context(), c.Param("name"), c.Param("taskId"))
	core.WriteResponse(c, resp, err)
}

// CancelA2ATask handles POST /v1/agent/agents/:name/tasks/:taskId/cancel.
func (h *Handler) CancelA2ATask(c *gin.Context) {
	err := h.biz.AgentV1().CancelA2ATask(c.Request.Context(), c.Param("name"), c.Param("taskId"))
	core.WriteResponse(c, nil, err)
}

// ListA2AInvocations handles GET /v1/agent/agents/:name/invocations.
func (h *Handler) ListA2AInvocations(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[v1.ListAgentA2AInvocationRequest](c),
		h.biz.AgentV1().ListA2AInvocations, h.val.ValidateListAgentA2AInvocationRequest)
}

// GetA2AInvocation handles GET /v1/agent/agents/:name/invocations/:invocationId.
func (h *Handler) GetA2AInvocation(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AgentV1().GetA2AInvocation, h.val.ValidateGetAgentA2AInvocationRequest)
}

func writeSSE(c *gin.Context, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = c.Writer.WriteString("data: " + string(payload) + "\n\n")
	c.Writer.Flush()
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		agents := engine.Group("/agent/agents")
		agents.POST("", h.CreateAgent)
		agents.GET("", h.ListAgent)
		// Register the static action before the parameterised GET.
		agents.POST("/preview", h.PreviewA2A)
		agents.GET("/:name", h.GetAgent)
		agents.PUT("/:name", h.UpdateAgent)
		agents.DELETE("/:name", h.DeleteAgent)
		// A2A invocation surface. The task/card endpoints live under the same
		// agent so a caller never has to discover a second resource.
		agents.POST("/:name/invoke", h.InvokeA2A)
		agents.POST("/:name/invoke/stream", h.InvokeA2AStream)
		agents.GET("/:name/tasks/:taskId", h.GetA2ATask)
		agents.POST("/:name/tasks/:taskId/cancel", h.CancelA2ATask)
		agents.GET("/:name/invocations", h.ListA2AInvocations)
		agents.GET("/:name/invocations/:invocationId", h.GetA2AInvocation)
		agents.POST("/:name/regenerate-prompt", h.RegenerateAgentPrompt)
		// The skills an agent exposes as commands. It is under the agent
		// rather than under /v1/agent/skills because the answer is per agent:
		// which skills a person can type depends on which repository records
		// that agent declares.
		agents.GET("/:name/context-skills", h.ListContextSkill)
	})
}
