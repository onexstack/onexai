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

	modelbiz "github.com/onexstack/onexai/internal/agent/biz/v1/model"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/restbind"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// CreateModel handles POST /v1/agent/models.
//
// The response is the one place the submitted plaintext key is returned, so it
// must not be cached by a proxy or the browser.
func (h *Handler) CreateModel(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	core.HandleJSONRequest(c, h.biz.ModelV1().Create, h.val.ValidateCreateModelRequest)
}

// GetModel handles GET /v1/agent/models/:name.
func (h *Handler) GetModel(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ModelV1().Get, h.val.ValidateGetModelRequest)
}

// ListModel handles GET /v1/agent/models.
func (h *Handler) ListModel(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.ModelV1().List, h.val.ValidateListModelRequest)
}

// RevealModelKey handles GET /v1/agent/models/:name/api-key.
//
// This is the one route that carries a model's plaintext key. Get and List
// deliberately omit it (see conversion.ModelModelToModelV1): the catalog is
// readable by every signed-in user, and a model key is an upstream credential
// that bills. The console calls this only when the owner presses "查看"; the
// answer is cached in that tab's memory, never in persistent storage.
func (h *Handler) RevealModelKey(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok || claims.UserID == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}

	req := new(v1.GetModelRequest)
	if err := core.ShouldBindUri(c, req, h.val.ValidateGetModelRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	// The response is a credential: make sure no proxy or browser cache keeps it.
	c.Header("Cache-Control", "no-store")
	key, err := h.biz.ModelV1().Reveal(c.Request.Context(), req.GetName(), modelbiz.Caller{
		UserID:  claims.UserID,
		Name:    claims.Name,
		IsAdmin: claims.IsAdmin,
	})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, gin.H{"api_key": key}, nil)
}

// UpdateModel handles PUT /v1/agent/models/:name.
func (h *Handler) UpdateModel(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateModelRequest) {
		if rq.GetModel() != nil {
			rq.Model.Name = c.Param("name") //nolint:protogetter // writing the request field
		}
	}), h.biz.ModelV1().Update, h.val.ValidateUpdateModelRequest)
}

// DeleteModel handles DELETE /v1/agent/models/:name.
func (h *Handler) DeleteModel(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.ModelV1().Delete, h.val.ValidateDeleteModelRequest)
}

// TestModel handles POST /v1/agent/models/test: one minimal completion against an
// endpoint, without persisting anything.
//
// The response is 200 with ok=false for a failed test: the request was valid,
// the endpoint was not. That is a different answer from a bind or storage
// failure, and the caller can show it without treating the API as broken.
func (h *Handler) TestModel(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.ModelV1().Test, h.val.ValidateTestModelRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		models := engine.Group("/agent/models")
		models.POST("", h.CreateModel)
		models.GET("", h.ListModel)
		// Register the static route before the parameterised one; both are
		// distinct methods from the GET below, so gin's tree is unambiguous.
		models.POST("/test", h.TestModel)
		models.GET("/:name", h.GetModel)
		// The key is a separate, on-demand read: the ordinary Get response must
		// stay free of it (it is served to every signed-in user).
		models.GET("/:name/api-key", h.RevealModelKey)
		models.PUT("/:name", h.UpdateModel)
		models.DELETE("/:name", h.DeleteModel)
	})
}
