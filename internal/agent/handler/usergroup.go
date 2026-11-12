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
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/agent/biz/v1/usergroup"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// The user-group surface is administrator-only.
//
// # Why there is a second admin check
//
// UserGuard refuses a non-administrator on the governance paths, but an AK/SK
// caller carries no claims and is let through by design. A service credential
// must not be able to rewrite group membership either, so the handler asks the
// question again and answers it for the "no claims" case too.

// ListUserGroup handles GET /v1/agent/usergroups.
func (h *Handler) ListUserGroup(c *gin.Context) {
	if !requireUserGroupAdmin(c) {
		return
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "0"))
	resp, err := h.biz.UserGroupV1().List(c.Request.Context(), &usergroup.ListRequest{
		Offset:  offset,
		Limit:   limit,
		Member:  c.Query("member"),
		Keyword: c.Query("keyword"),
	})
	core.WriteResponse(c, resp, err)
}

// GetUserGroup handles GET /v1/agent/usergroups/:name.
func (h *Handler) GetUserGroup(c *gin.Context) {
	if !requireUserGroupAdmin(c) {
		return
	}
	group, err := h.biz.UserGroupV1().Get(c.Request.Context(), c.Param("name"))
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, gin.H{"userGroup": group}, nil)
}

// CreateUserGroup handles POST /v1/agent/usergroups.
func (h *Handler) CreateUserGroup(c *gin.Context) {
	if !requireUserGroupAdmin(c) {
		return
	}
	req, ok := bindUserGroup(c)
	if !ok {
		return
	}
	req.Owner = callerID(c)
	group, err := h.biz.UserGroupV1().Create(c.Request.Context(), req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, gin.H{"userGroup": group}, nil)
}

// UpdateUserGroup handles PUT /v1/agent/usergroups/:name. The path names the
// group; a name in the body is ignored, as it is for every other resource.
func (h *Handler) UpdateUserGroup(c *gin.Context) {
	if !requireUserGroupAdmin(c) {
		return
	}
	req, ok := bindUserGroup(c)
	if !ok {
		return
	}
	req.Name = c.Param("name")
	// The owner is audit metadata: an update must not transfer it to whoever
	// happened to save the row last.
	req.Owner = ""
	group, err := h.biz.UserGroupV1().Update(c.Request.Context(), req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, gin.H{"userGroup": group}, nil)
}

// DeleteUserGroup handles DELETE /v1/agent/usergroups/:name.
func (h *Handler) DeleteUserGroup(c *gin.Context) {
	if !requireUserGroupAdmin(c) {
		return
	}
	if err := h.biz.UserGroupV1().Delete(c.Request.Context(), c.Param("name")); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, gin.H{"ok": true}, nil)
}

// bindUserGroup reads the `{ "userGroup": { ... } }` envelope the console's
// resource layer writes.
func bindUserGroup(c *gin.Context) (*usergroup.CreateRequest, bool) {
	var body struct {
		UserGroup *usergroup.CreateRequest `json:"userGroup"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("%s", err.Error()))
		return nil, false
	}
	if body.UserGroup == nil {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("缺少 userGroup"))
		return nil, false
	}
	return body.UserGroup, true
}

// requireUserGroupAdmin writes a refusal and returns false unless the caller is
// an administrator. It aborts the chain so a handler bug cannot fall through
// into the write.
func requireUserGroupAdmin(c *gin.Context) bool {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok || claims == nil || !claims.IsAdmin {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("只有管理员可以管理用户组"))
		c.Abort()
		return false
	}
	return true
}

// callerID returns the account ID that should own a newly created group.
func callerID(c *gin.Context) string {
	if claims, ok := bearer.ClaimsFrom(c); ok && claims != nil {
		return claims.UserID
	}
	return ""
}

// GetUserGroupMemberships handles
// GET /v1/agent/usergroup-memberships?identity=...
//
// It is a service-to-service lookup rather than a console route: chat asks it
// while resolving whether an identity is an administrator or an SRE, and the
// signed service group is the surface that already authenticates that caller.
// The path is deliberately not a child of /agent/usergroups, where `:name`
// would claim it.
func (h *Handler) GetUserGroupMemberships(c *gin.Context) {
	identity := strings.TrimSpace(c.Query("identity"))
	if identity == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument.WithMessage("缺少 identity"))
		return
	}
	codes, err := h.biz.UserGroupV1().CodesFor(c.Request.Context(), identity)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, gin.H{"codes": codes}, nil)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		groups := engine.Group("/agent/usergroups")
		groups.GET("", h.ListUserGroup)
		groups.POST("", h.CreateUserGroup)
		groups.GET("/:name", h.GetUserGroup)
		groups.PUT("/:name", h.UpdateUserGroup)
		groups.DELETE("/:name", h.DeleteUserGroup)
	})
	// Signed service callers only: chat resolves group membership for role
	// checks with its AK/SK pair.
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		engine.GET("/agent/usergroup-memberships", h.GetUserGroupMemberships)
	})
}
