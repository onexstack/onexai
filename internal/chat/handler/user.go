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
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	userbiz "github.com/onexstack/onexai/internal/chat/biz/v1/user"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// GetUser handles GET /v1/users/:id.
func (h *Handler) GetUser(c *gin.Context) {
	resp, err := h.biz.UserV1().Get(c.Request.Context(), &userbiz.GetRequest{
		UserID: c.Param("id"),
	})
	core.WriteResponse(c, gin.H{"user": resp}, err)
}

// ListUsers handles GET /v1/users.
func (h *Handler) ListUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	resp, err := h.biz.UserV1().List(c.Request.Context(), &userbiz.ListRequest{
		Page:     page,
		PageSize: pageSize,
		Keyword:  c.Query("keyword"),
		Status:   c.Query("status"),
		Role:     c.Query("role"),
	})
	core.WriteResponse(c, resp, err)
}

// CreateUser handles POST /v1/users.
func (h *Handler) CreateUser(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
		Status      string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	resp, err := h.biz.UserV1().Create(c.Request.Context(), &userbiz.CreateRequest{
		ActorID:     claims.UserID,
		Username:    req.Username,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Role:        req.Role,
		Status:      req.Status,
	})
	core.WriteResponse(c, resp, err)
}

// DeleteUser handles DELETE /v1/users/:id.
func (h *Handler) DeleteUser(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	err := h.biz.UserV1().Delete(c.Request.Context(), &userbiz.DeleteRequest{
		ActorID: claims.UserID,
		UserID:  c.Param("id"),
	})
	core.WriteResponse(c, gin.H{"ok": err == nil}, err)
}

// UpdateUser handles PATCH /v1/users/:id.
func (h *Handler) UpdateUser(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	var req struct {
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
		Status      *string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	resp, err := h.biz.UserV1().Update(c.Request.Context(), &userbiz.UpdateRequest{
		ActorID:     claims.UserID,
		UserID:      c.Param("id"),
		DisplayName: req.DisplayName,
		Role:        req.Role,
		Status:      req.Status,
	})
	core.WriteResponse(c, resp, err)
}

// ResetUserPassword handles POST /v1/users/:id/password.
func (h *Handler) ResetUserPassword(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	err := h.biz.UserV1().ResetPassword(c.Request.Context(), &userbiz.ResetPasswordRequest{
		ActorID:     claims.UserID,
		UserID:      c.Param("id"),
		NewPassword: req.NewPassword,
	})
	core.WriteResponse(c, gin.H{"ok": err == nil}, err)
}

// FeishuBindStart handles POST /v1/auth/feishu/bind/start.
func (h *Handler) FeishuBindStart(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	resp, err := h.biz.UserV1().FeishuBindStart(c.Request.Context(), &userbiz.BindStartRequest{
		UserID: claims.UserID,
	})
	core.WriteResponse(c, resp, err)
}

// FeishuBindStatus handles GET /v1/auth/feishu/bind/status?code=...
func (h *Handler) FeishuBindStatus(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	resp, err := h.biz.UserV1().FeishuBindStatus(c.Request.Context(), &userbiz.BindStatusRequest{
		UserID: claims.UserID,
		Code:   c.Query("code"),
	})
	core.WriteResponse(c, resp, err)
}

// FeishuBindConfirm handles POST /v1/auth/feishu/bind/confirm.
func (h *Handler) FeishuBindConfirm(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	var req struct {
		Code    string `json:"code"`
		Confirm string `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	resp, err := h.biz.UserV1().FeishuBindConfirm(c.Request.Context(), &userbiz.BindConfirmRequest{
		UserID:  claims.UserID,
		Code:    req.Code,
		Confirm: req.Confirm,
	})
	core.WriteResponse(c, resp, err)
}

// FeishuUnbind handles DELETE /v1/auth/feishu/bind.
func (h *Handler) FeishuUnbind(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	err := h.biz.UserV1().FeishuUnbind(c.Request.Context(), &userbiz.UnbindRequest{
		UserID: claims.UserID,
	})
	core.WriteResponse(c, gin.H{"ok": err == nil}, err)
}

// FeishuBindings handles GET /v1/auth/feishu/bindings.
func (h *Handler) FeishuBindings(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	resp, err := h.biz.UserV1().FeishuBindings(c.Request.Context(), &userbiz.BindingsRequest{
		UserID: claims.UserID,
	})
	core.WriteResponse(c, resp, err)
}

// ListFeishuBindings handles GET /v1/feishu-bindings.
func (h *Handler) ListFeishuBindings(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	resp, err := h.biz.UserV1().ListFeishuBindings(
		c.Request.Context(), &userbiz.ListFeishuBindingsRequest{
			ActorID:  claims.UserID,
			IsAdmin:  claims.IsAdmin,
			Page:     page,
			PageSize: pageSize,
			Keyword:  c.Query("keyword"),
			Source:   c.Query("source"),
			UserID:   c.Query("user_id"),
		})
	core.WriteResponse(c, resp, err)
}

// GetFeishuBinding handles GET /v1/feishu-bindings/:peerID.
func (h *Handler) GetFeishuBinding(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	resp, err := h.biz.UserV1().GetFeishuBinding(
		c.Request.Context(), &userbiz.GetFeishuBindingRequest{
			ActorID: claims.UserID,
			IsAdmin: claims.IsAdmin,
			PeerID:  c.Param("peerID"),
		})
	core.WriteResponse(c, resp, err)
}

// UpdateFeishuAgents handles PUT /v1/feishu-bindings/:peerID/agents.
func (h *Handler) UpdateFeishuAgents(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	// A pointer distinguishes "clear the list" from "the field was omitted":
	// both decode to a nil slice, but only one of them is a request to wipe the
	// binding's capabilities.
	var req struct {
		Agents *[]string `json:"agents"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	if req.Agents == nil {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("缺少 agents 字段"))
		return
	}
	resp, err := h.biz.UserV1().UpdateFeishuAgents(
		c.Request.Context(), &userbiz.UpdateFeishuAgentsRequest{
			ActorID: claims.UserID,
			IsAdmin: claims.IsAdmin,
			PeerID:  c.Param("peerID"),
			Agents:  *req.Agents,
		})
	core.WriteResponse(c, resp, err)
}

// UnbindFeishuBinding handles DELETE /v1/feishu-bindings/:peerID.
func (h *Handler) UnbindFeishuBinding(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	err := h.biz.UserV1().UnbindFeishuBinding(
		c.Request.Context(), &userbiz.UnbindFeishuBindingRequest{
			ActorID: claims.UserID,
			IsAdmin: claims.IsAdmin,
			PeerID:  c.Param("peerID"),
		})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		// Self-service Feishu binding. The identity is always the caller's
		// claims, never a request parameter: binding somebody else's account is
		// not a feature.
		feishu := engine.Group("/auth/feishu", h.AuthMiddleware())
		feishu.POST("/bind/start", h.FeishuBindStart)
		feishu.GET("/bind/status", h.FeishuBindStatus)
		feishu.POST("/bind/confirm", h.FeishuBindConfirm)
		feishu.DELETE("/bind", h.FeishuUnbind)
		feishu.GET("/bindings", h.FeishuBindings)

		// Peer-binding management. One group serves both audiences: the handler
		// passes the caller's role and the biz narrows the visible rows. Hanging
		// RequireAdmin here would make self-service impossible.
		bindings := engine.Group("/feishu-bindings", h.AuthMiddleware())
		bindings.GET("", h.ListFeishuBindings)
		bindings.GET("/:peerID", h.GetFeishuBinding)
		bindings.PUT("/:peerID/agents", h.UpdateFeishuAgents)
		bindings.DELETE("/:peerID", h.UnbindFeishuBinding)

		// Administration. RequireAdmin reads the claims the auth middleware
		// placed in the context, so its position in the chain is load-bearing.
		users := engine.Group("/users", h.AuthMiddleware(), bearer.RequireAdmin())
		users.GET("", h.ListUsers)
		users.GET("/:id", h.GetUser)
		users.POST("", h.CreateUser)
		users.PATCH("/:id", h.UpdateUser)
		users.DELETE("/:id", h.DeleteUser)
		users.POST("/:id/password", h.ResetUserPassword)
	})
}
