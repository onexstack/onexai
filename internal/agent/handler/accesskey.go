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

	"github.com/onexstack/onexai/internal/agent/biz/v1/aksk"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// The methods here implement the owner's own credential management.
//
// # The owner is never a request field
//
// Every route reads the caller's user ID from the bearer claims. There is no
// path that takes somebody else's ID, because "read another account's secret"
// is not a feature; an administrator still cannot see a credential they did
// not create.

// ListAccessKey handles GET /v1/aksk.
func (h *Handler) ListAccessKey(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok || claims.UserID == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	resp, err := h.biz.AccessKeyV1().List(c.Request.Context(), claims.UserID)
	core.WriteResponse(c, resp, err)
}

// CreateAccessKey handles POST /v1/aksk.
func (h *Handler) CreateAccessKey(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok || claims.UserID == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	resp, err := h.biz.AccessKeyV1().Create(c.Request.Context(), &aksk.CreateRequest{
		UserID: claims.UserID,
		Name:   req.Name,
	})
	core.WriteResponse(c, resp, err)
}

// RevealAccessKey handles GET /v1/aksk/:id.
//
// This is the one route that carries the plaintext. The list does not, on
// purpose: loading a page should not put every live credential in the same
// response.
func (h *Handler) RevealAccessKey(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok || claims.UserID == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	secret, err := h.biz.AccessKeyV1().Reveal(
		c.Request.Context(), claims.UserID, c.Param("id"))
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, gin.H{"secret_key": secret}, nil)
}

// UpdateAccessKey handles PUT /v1/aksk/:id.
//
// The body names the target state rather than "toggle": sending the same value
// twice is still the same result, which a flip is not when two tabs race.
func (h *Handler) UpdateAccessKey(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok || claims.UserID == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return
	}
	if req.Enabled == nil {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("缺少 enabled"))
		return
	}
	err := h.biz.AccessKeyV1().SetEnabled(
		c.Request.Context(), claims.UserID, c.Param("id"), *req.Enabled)
	core.WriteResponse(c, gin.H{"ok": err == nil}, err)
}

// DeleteAccessKey handles DELETE /v1/aksk/:id.
func (h *Handler) DeleteAccessKey(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok || claims.UserID == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	err := h.biz.AccessKeyV1().Delete(c.Request.Context(), claims.UserID, c.Param("id"))
	core.WriteResponse(c, gin.H{"ok": err == nil}, err)
}

func init() {
	RegisterSelfService(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		aksk := engine.Group("/aksk")
		aksk.GET("", h.ListAccessKey)
		aksk.POST("", h.CreateAccessKey)
		aksk.GET("/:id", h.RevealAccessKey)
		aksk.PUT("/:id", h.UpdateAccessKey)
		aksk.DELETE("/:id", h.DeleteAccessKey)
	})
}
