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

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// CreateClusterAgentBinding handles POST /v1/agent/clusteragentbindings.
func (h *Handler) CreateClusterAgentBinding(c *gin.Context) {
	if !requireClusterBindingAdmin(c) {
		return
	}
	core.HandleJSONRequest(c, h.biz.ClusterAgentBindingV1().Create,
		h.val.ValidateCreateClusterAgentBindingRequest)
}

// GetClusterAgentBinding handles GET /v1/agent/clusteragentbindings/:name.
func (h *Handler) GetClusterAgentBinding(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ClusterAgentBindingV1().Get, h.val.ValidateGetClusterAgentBindingRequest)
}

// ListClusterAgentBinding handles GET /v1/agent/clusteragentbindings.
func (h *Handler) ListClusterAgentBinding(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.ClusterAgentBindingV1().List,
		h.val.ValidateListClusterAgentBindingRequest)
}

// UpdateClusterAgentBinding handles PUT /v1/agent/clusteragentbindings/:name.
func (h *Handler) UpdateClusterAgentBinding(c *gin.Context) {
	if !requireClusterBindingAdmin(c) {
		return
	}
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateClusterAgentBindingRequest) {
		if rq.GetClusterAgentBinding() != nil {
			rq.ClusterAgentBinding.Name = c.Param("name") //nolint:protogetter // writing the request field
		}
	}), h.biz.ClusterAgentBindingV1().Update, h.val.ValidateUpdateClusterAgentBindingRequest)
}

// DeleteClusterAgentBinding handles DELETE /v1/agent/clusteragentbindings/:name.
func (h *Handler) DeleteClusterAgentBinding(c *gin.Context) {
	if !requireClusterBindingAdmin(c) {
		return
	}
	core.HandleUriRequest(c, h.biz.ClusterAgentBindingV1().Delete, h.val.ValidateDeleteClusterAgentBindingRequest)
}

// requireClusterBindingAdmin refuses a write from anyone but an administrator.
//
// A cluster-wide binding is the platform's batch grant: one object can hand a
// capability to every agent in a list of tenants. The namespaced binding is
// gated by read access to its targets (see the control-plane Admission), but a
// cluster binding has no namespace of its own to bound that check, so the
// authority has to be explicit here.
//
// Reads stay open to every logged-in user, like the rest of the declarative
// surface: an operator must be able to see which platform preset applies where.
func requireClusterBindingAdmin(c *gin.Context) bool {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok || claims.UserID == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return false
	}
	if !claims.IsAdmin {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage(
			"只有管理员可以管理平台级能力绑定"))
		return false
	}
	return true
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		bindings := engine.Group("/agent/clusteragentbindings")
		bindings.POST("", h.CreateClusterAgentBinding)
		bindings.GET("", h.ListClusterAgentBinding)
		bindings.GET("/:name", h.GetClusterAgentBinding)
		bindings.PUT("/:name", h.UpdateClusterAgentBinding)
		bindings.DELETE("/:name", h.DeleteClusterAgentBinding)
	})
}
