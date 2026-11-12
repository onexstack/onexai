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

// CreateSecret handles POST /v1/core/secrets.
func (h *Handler) CreateSecret(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.SecretV1().Create, h.val.ValidateCreateSecretRequest)
}

// GetSecret handles GET /v1/core/secrets/:secretId.
func (h *Handler) GetSecret(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.SecretV1().Get, h.val.ValidateGetSecretRequest)
}

// RevealSecretKey handles GET /v1/core/secrets/:secretId/key.
//
// Secrets are a governance resource, so UserGuard already restricts every
// method under /v1/core/secrets to administrators. That check only applies to
// callers with claims, though: an AK/SK service caller has none and passes the
// guard. This route answers with plaintext, so it refuses those callers
// explicitly rather than widening what the service face can read.
func (h *Handler) RevealSecretKey(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok || claims.UserID == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	if !claims.IsAdmin {
		core.WriteResponse(c, nil, errno.ErrPermissionDenied.WithMessage("只有管理员可以查看凭据密钥"))
		return
	}

	c.Header("Cache-Control", "no-store")
	key, err := h.biz.SecretV1().RevealKey(c.Request.Context(), c.Param("secretId"))
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, gin.H{"secret_key": key}, nil)
}

// ListSecret handles GET /v1/core/secrets.
func (h *Handler) ListSecret(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.SecretV1().List, h.val.ValidateListSecretRequest)
}

// UpdateSecret handles PUT /v1/core/secrets/:secretId.
func (h *Handler) UpdateSecret(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateSecretRequest) {
		if rq.GetSecret() != nil {
			rq.Secret.SecretId = c.Param("secretId") //nolint:protogetter // writing the request field
		}
	}), h.biz.SecretV1().Update, h.val.ValidateUpdateSecretRequest)
}

// DeleteSecret handles DELETE /v1/core/secrets/:secretId.
func (h *Handler) DeleteSecret(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.SecretV1().Delete, h.val.ValidateDeleteSecretRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		secrets := engine.Group("/core/secrets")
		secrets.POST("", h.CreateSecret)
		secrets.GET("", h.ListSecret)
		secrets.GET("/:secretId", h.GetSecret)
		secrets.GET("/:secretId/key", h.RevealSecretKey)
		secrets.PUT("/:secretId", h.UpdateSecret)
		secrets.DELETE("/:secretId", h.DeleteSecret)
	})
}
