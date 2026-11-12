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

	sharebiz "github.com/onexstack/onexai/internal/agent/biz/v1/share"
	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// CreateSessionShare handles POST /v1/agent/sessions/:id/shares.
func (h *Handler) CreateSessionShare(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[v1.CreateSessionShareParams](c),
		h.biz.ShareV1().Create, h.val.ValidateCreateSessionShareRequest)
}

// ListSessionShare handles GET /v1/agent/sessions/:id/shares.
func (h *Handler) ListSessionShare(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ShareV1().List, h.val.ValidateListSessionShareRequest)
}

// RevokeSessionShare handles DELETE /v1/agent/sessions/:id/shares/:shareID.
func (h *Handler) RevokeSessionShare(c *gin.Context) {
	var req v1.RevokeSessionShareRequest
	if err := core.ReadRequest(c, &req, c.ShouldBindUri, h.val.ValidateRevokeSessionShareRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if err := h.biz.ShareV1().Revoke(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// SharedAgentRun handles GET /v1/share/runs/:id?token=...
func (h *Handler) SharedAgentRun(c *gin.Context) {
	core.HandleUriRequest(c, func(ctx context.Context, req *v1.SharedRunParams) (*sharebiz.SharedRunResponse, error) {
		return h.biz.ShareV1().SharedRun(ctx, req.ID, shareToken(c))
	}, h.val.ValidateSharedRunRequest)
}

// SharedRunEvents handles GET /v1/share/runs/:id/events?token=...
func (h *Handler) SharedRunEvents(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[v1.SharedRunEventsQuery](c),
		func(ctx context.Context, req *v1.SharedRunEventsQuery) (*sharebiz.SharedEventsResponse, error) {
			return h.biz.ShareV1().SharedRunEvents(ctx, req.ID, req.After, shareToken(c))
		}, h.val.ValidateSharedRunEventsRequest)
}

// SharedSessionRuns handles GET /v1/share/sessions/:id/runs?token=...
func (h *Handler) SharedSessionRuns(c *gin.Context) {
	core.HandleUriRequest(c, func(ctx context.Context, req *v1.SharedSessionRunsParams) (*sharebiz.SharedRunsResponse, error) {
		return h.biz.ShareV1().SharedSessionRuns(ctx, req.ID, shareToken(c))
	}, h.val.ValidateSharedSessionRunsRequest)
}

// shareToken reads the credential from the query, falling back to the header.
func shareToken(c *gin.Context) string {
	if token := c.Query("token"); token != "" {
		return token
	}
	return c.GetHeader("X-Onex-Share-Token")
}

func init() { //nolint:gochecknoinits
	Register(func(v1 *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		v1.POST("/agent/sessions/:id/shares", h.CreateSessionShare)
		v1.GET("/agent/sessions/:id/shares", h.ListSessionShare)
		v1.DELETE("/agent/sessions/:id/shares/:shareID", h.RevokeSessionShare)
	})
}
