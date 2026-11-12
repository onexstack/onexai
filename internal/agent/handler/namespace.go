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

	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// CreateNamespace handles POST /v1/agent/namespaces.
func (h *Handler) CreateNamespace(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.NamespaceV1().Create, h.val.ValidateCreateNamespaceRequest)
}

// GetNamespace handles GET /v1/agent/namespaces/:name.
func (h *Handler) GetNamespace(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.NamespaceV1().Get, h.val.ValidateGetNamespaceRequest)
}

// ListNamespace handles GET /v1/agent/namespaces.
//
// The console uses it both as the business-line page's list and as the
// candidate source for every resource's namespace field.
func (h *Handler) ListNamespace(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.NamespaceV1().List, h.val.ValidateListNamespaceRequest)
}

// UpdateNamespace handles PUT /v1/agent/namespaces/:name.
func (h *Handler) UpdateNamespace(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateNamespaceRequest) {
		if rq.GetNamespace() != nil {
			rq.Namespace.Name = c.Param("name") //nolint:protogetter // writing the request field
		}
	}), h.biz.NamespaceV1().Update, h.val.ValidateUpdateNamespaceRequest)
}

// DeleteNamespace handles DELETE /v1/agent/namespaces/:name.
func (h *Handler) DeleteNamespace(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.NamespaceV1().Delete, h.val.ValidateDeleteNamespaceRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		namespaces := engine.Group("/agent/namespaces")
		namespaces.POST("", h.CreateNamespace)
		namespaces.GET("", h.ListNamespace)
		namespaces.GET("/:name", h.GetNamespace)
		namespaces.PUT("/:name", h.UpdateNamespace)
		namespaces.DELETE("/:name", h.DeleteNamespace)
	})
}
