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

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/chat/pkg/validation"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/errno"
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// maxAssetUploadBytes bounds an upload body on this hop.
//
// The backend applies the per-kind ceilings to the decoded bytes and is the
// authority on them. This is the earlier bound: a request that declares a
// gigabyte is refused before this process allocates it, rather than after.
const maxAssetUploadBytes = 20 << 20 // 20 MiB

// UploadAsset handles POST /v1/assets — store an image and return the reference
// that names it.
//
// # Why this service has an upload endpoint at all
//
// The store belongs to oai-agent-apiserver, and the obvious design is for a
// client to call that service directly. A TUI cannot: it holds a bearer token
// for *this* service and has no key to sign an agent request with. Without this
// route the client can be told a message may carry images and have no way to
// obtain one — which is what the WS send frame's images field was until this
// existed.
//
// # The owner is stamped here, not read from the body
//
// The backend trusts its callers about ownership, because they are services
// with a signed request and it has nothing else to go on. This service is the
// one that knows who is actually asking, so it overwrites whatever the body
// said: a client that named somebody else would otherwise store an image under
// their identity and be able to read it back as them.
func (h *Handler) UploadAsset(c *gin.Context) {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAssetUploadBytes)
	var req agentv1.CreateAssetRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateCreateAssetRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	// The caller's own identity wins, whatever the body claimed. Source and
	// Kind were filled by the generated Default().
	req.OwnerID = claims.UserID

	asset, err := h.biz.ChatV1().UploadAsset(c.Request.Context(), &req)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, gin.H{"asset": asset}, nil)
}

// GetAssetContent handles GET /v1/assets/:assetID/content — an asset's bytes.
//
// It is the read half of the same window. GET /v1/images/:msgID serves an image
// that arrived from Lark, addressed by the platform's key; this serves one the
// store holds, addressed by its reference. A client rendering a message needs
// both, since a conversation can contain either.
func (h *Handler) GetAssetContent(c *gin.Context) {
	var req validation.GetAssetContentRequest
	if err := core.ShouldBindUri(c, &req, h.val.ValidateGetAssetContentRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	data, contentType, err := h.biz.ChatV1().FetchAssetContent(c.Request.Context(), req.AssetID)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	c.Data(http.StatusOK, contentType, data)
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		authed := engine.Group("", h.AuthMiddleware())
		authed.POST("/assets", h.UploadAsset)
		authed.GET("/assets/:assetID/content", h.GetAssetContent)
	})
}
