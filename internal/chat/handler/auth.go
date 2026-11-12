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

	authn "github.com/onexstack/onexai/internal/chat/biz/v1/auth"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// Login handles POST /v1/auth/login.
func (h *Handler) Login(c *gin.Context) {
	var req authn.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}

	resp, err := h.biz.AuthV1().Login(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

// Register handles POST /v1/auth/register.
func (h *Handler) Register(c *gin.Context) {
	var req authn.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	resp, err := h.biz.AuthV1().Register(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

// Providers handles GET /v1/auth/providers.
//
// It is public because the login form has to decide what to draw before it has
// a token to call anything with.
func (h *Handler) Providers(c *gin.Context) {
	core.WriteResponse(c, h.biz.AuthV1().Providers(c.Request.Context()), nil)
}

// LarkAuthorizeURL handles GET /v1/auth/lark/authorize-url?redirect=/path.
func (h *Handler) LarkAuthorizeURL(c *gin.Context) {
	req := &authn.LarkAuthorizeURLRequest{Redirect: c.Query("redirect")}
	resp, err := h.biz.AuthV1().LarkAuthorizeURL(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

// LarkLogin handles POST /v1/auth/lark/login — the callback exchange.
func (h *Handler) LarkLogin(c *gin.Context) {
	var req authn.LarkLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	resp, err := h.biz.AuthV1().LarkLogin(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

// Me handles GET /v1/auth/me.
func (h *Handler) Me(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	resp, err := h.biz.AuthV1().Me(c.Request.Context(), &authn.MeRequest{UserID: claims.UserID})
	core.WriteResponse(c, resp, err)
}

// ChangePassword handles POST /v1/auth/password.
func (h *Handler) ChangePassword(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	err := h.biz.AuthV1().ChangePassword(c.Request.Context(), &authn.ChangePasswordRequest{
		UserID:      claims.UserID,
		OldPassword: req.OldPassword,
		NewPassword: req.NewPassword,
	})
	core.WriteResponse(c, gin.H{"ok": err == nil}, err)
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		rg := engine.Group("/auth")
		// Public: these are what a caller uses to obtain a token.
		rg.POST("login", h.Login)
		rg.POST("register", h.Register)
		rg.GET("providers", h.Providers)
		rg.GET("lark/authorize-url", h.LarkAuthorizeURL)
		rg.POST("lark/login", h.LarkLogin)

		// Authenticated: these describe or change the caller's own account.
		authed := rg.Group("", h.AuthMiddleware())
		authed.GET("me", h.Me)
		authed.POST("password", h.ChangePassword)
	})
}
