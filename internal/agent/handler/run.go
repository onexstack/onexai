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
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	runbiz "github.com/onexstack/onexai/internal/agent/biz/v1/run"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/restbind"
	"github.com/onexstack/onexai/internal/pkg/safego"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// createAgentRunRequest is the JSON body of POST /v1/agent/runs. It is a plain
// DTO rather than a proto because it belongs to the internal scheduling
// contract, not to the public OpenAI/agent API.
type createAgentRunRequest struct {
	DedupKey         string            `json:"dedupKey"`
	Agent            string            `json:"agent"`
	Prompt           string            `json:"prompt"`
	SubjectID        string            `json:"subjectID"`
	SubjectName      string            `json:"subjectName"`
	SubjectKind      string            `json:"subjectKind"`
	Metering         string            `json:"metering"`
	ApprovalFallback string            `json:"approvalFallback"`
	SessionPolicy    string            `json:"sessionPolicy"`
	Source           string            `json:"source"`
	Trigger          string            `json:"trigger"`
	TimeoutSeconds   int64             `json:"timeoutSeconds"`
	ScheduleRef      string            `json:"scheduleRef"`
	ScheduledTime    int64             `json:"scheduledTime"`
	Attempt          int32             `json:"attempt"`
	Metadata         map[string]string `json:"metadata"`
}

type createAgentRunResponse struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	State     string `json:"state"`
}

// CreateAgentRun handles POST /v1/agent/runs. It records a durable admission
// and returns 202; execution happens in the background. A duplicate dedupKey
// returns the existing run with 200, so a retried dispatch is safe.
func (h *Handler) CreateAgentRun(c *gin.Context) {
	var req createAgentRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("%s", err.Error()))
		return
	}
	if req.Agent == "" || req.Prompt == "" || req.SubjectID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("agent, prompt and subjectID are required"))
		return
	}

	admission, created, err := h.biz.RunV1().Admit(c.Request.Context(), &runbiz.AdmitRequest{
		DedupKey:         req.DedupKey,
		Agent:            req.Agent,
		Prompt:           req.Prompt,
		SubjectID:        req.SubjectID,
		SubjectName:      req.SubjectName,
		SubjectKind:      req.SubjectKind,
		Metering:         req.Metering,
		ApprovalFallback: req.ApprovalFallback,
		SessionPolicy:    req.SessionPolicy,
		Source:           req.Source,
		Trigger:          req.Trigger,
		TimeoutSeconds:   req.TimeoutSeconds,
		ScheduleRef:      req.ScheduleRef,
		ScheduledTime:    req.ScheduledTime,
		Attempt:          req.Attempt,
		Metadata:         req.Metadata,
	})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if created {
		safego.Go(context.Background(), "run execute "+admission.ID, func() {
			h.biz.RunV1().Execute(context.Background(), admission.ID)
		})
		c.JSON(http.StatusAccepted, createAgentRunResponse{
			ID: admission.ID, SessionID: admission.SessionID, State: admission.State,
		})
		return
	}
	c.JSON(http.StatusOK, createAgentRunResponse{
		ID: admission.ID, SessionID: admission.SessionID, State: admission.State,
	})
}

// GetAgentRun handles GET /v1/agent/runs/:id.
func (h *Handler) GetAgentRun(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.RunV1().Get, h.val.ValidateGetRunRequest)
}

// ListSessionRun handles GET /v1/agent/sessions/:id/runs.
func (h *Handler) ListSessionRun(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[v1.ListSessionRunQuery](c),
		h.biz.RunV1().ListSessionRun, h.val.ValidateListSessionRunRequest)
}

// ListAgentRunEvent handles GET /v1/agent/runs/:id/events?after=<seq>.
func (h *Handler) ListAgentRunEvent(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[v1.ListRunEventQuery](c),
		h.biz.RunV1().ListEvent, h.val.ValidateListRunEventRequest)
}

// ListAgentApproval handles GET /v1/agent/approvals.
func (h *Handler) ListAgentApproval(c *gin.Context) {
	core.HandleRequest(c, c.ShouldBindQuery, h.biz.RunV1().ListApproval, h.val.ValidateListApprovalRequest)
}

// CancelAgentRun handles POST /v1/agent/runs/:id/cancel.
func (h *Handler) CancelAgentRun(c *gin.Context) {
	var req v1.CancelRunRequest
	if err := core.ReadRequest(c, &req, c.ShouldBindUri, h.val.ValidateCancelRunRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if err := h.biz.RunV1().Cancel(c.Request.Context(), &req); err != nil {
		writeRunError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func init() { //nolint:gochecknoinits
	Register(func(v1 *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		v1.POST("/agent/runs", h.CreateAgentRun)
		v1.GET("/agent/runs/:id", h.GetAgentRun)
		v1.GET("/agent/runs/:id/events", h.ListAgentRunEvent)
		v1.POST("/agent/runs/:id/cancel", h.CancelAgentRun)
		v1.GET("/agent/sessions/:id/runs", h.ListSessionRun)
		v1.GET("/agent/approvals", h.ListAgentApproval)
	})
}
