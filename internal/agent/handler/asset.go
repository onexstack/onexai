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
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/agent/biz/v1/asset"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// MediaHandler is the slice of a store this layer serves objects with.
//
// It is an interface rather than *media.LocalStore so that only the local
// driver has to satisfy it: an object store serves its own bytes, so the
// service has no route to mount, and the handler holds nil.
type MediaHandler interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

// maxUploadBytes bounds an upload body.
//
// The per-kind ceilings are the real limit and are checked on the decoded
// bytes. This is the one applied before anything is decoded, so a request that
// declares a gigabyte is refused at the door rather than after the process has
// allocated it — base64 expands by a third, hence the slack.
const maxUploadBytes = 20 << 20 // 20 MiB

// CreateAsset handles POST /v1/assets — store an uploaded image.
func (h *Handler) CreateAsset(c *gin.Context) {
	if h.biz == nil || h.biz.Asset() == nil || !h.biz.Asset().Configured() {
		core.WriteResponse(c, nil, errno.ErrNotConfigured)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadBytes)
	core.HandleJSONRequest(c, h.createAsset, h.val.ValidateCreateAssetRequest)
}

func (h *Handler) createAsset(ctx context.Context, req *v1.CreateAssetRequest) (gin.H, error) {
	out, err := h.biz.Asset().Create(ctx, req)
	if err != nil {
		return nil, assetErrno(err)
	}

	return gin.H{"asset": out}, nil
}

// GetAsset handles GET /v1/assets/:assetID.
func (h *Handler) GetAsset(c *gin.Context) {
	if h.biz == nil || h.biz.Asset() == nil || !h.biz.Asset().Configured() {
		core.WriteResponse(c, nil, errno.ErrNotConfigured)
		return
	}
	var req v1.GetAssetRequest
	if err := core.ShouldBindUri(c, &req, h.val.ValidateGetAssetRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	out, err := h.biz.Asset().Get(c.Request.Context(), req.GetAssetId())
	if err != nil {
		core.WriteResponse(c, nil, assetErrno(err))
		return
	}
	core.WriteResponse(c, gin.H{"asset": out}, nil)
}

// GetAssetContent handles GET /v1/assets/:assetID/content — the asset's bytes.
//
// It is separate from the /media route rather than the same thing with a
// different path. That route serves whatever the local driver holds and is
// unauthenticated so a browser can render a message; this one is signed, goes
// through the service, and works whichever driver is configured — which is what
// a service-to-service caller needs, since it cannot reach an object store's
// internal address and has a key to sign with.
func (h *Handler) GetAssetContent(c *gin.Context) {
	if h.biz == nil || h.biz.Asset() == nil || !h.biz.Asset().Configured() {
		core.WriteResponse(c, nil, errno.ErrNotConfigured)
		return
	}
	var req v1.GetAssetRequest
	if err := core.ShouldBindUri(c, &req, h.val.ValidateGetAssetRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	data, contentType, err := h.biz.Asset().Content(c.Request.Context(), req.GetAssetId())
	if err != nil {
		core.WriteResponse(c, nil, assetErrno(err))
		return
	}
	c.Data(http.StatusOK, contentType, data)
}

// DeleteAsset handles DELETE /v1/assets/:assetID.
func (h *Handler) DeleteAsset(c *gin.Context) {
	if h.biz == nil || h.biz.Asset() == nil || !h.biz.Asset().Configured() {
		core.WriteResponse(c, nil, errno.ErrNotConfigured)
		return
	}
	var req v1.DeleteAssetRequest
	if err := core.ShouldBindUri(c, &req, h.val.ValidateDeleteAssetRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if err := h.biz.Asset().Delete(c.Request.Context(), req.GetAssetId()); err != nil {
		core.WriteResponse(c, nil, assetErrno(err))
		return
	}
	core.WriteResponse(c, gin.H{}, nil)
}

// ServeMedia handles GET /media/*objectKey — the bytes of an object this
// service stores itself.
//
// # Why it is not under /v1
//
// Every /v1 route is signed, and a browser rendering a message has no key to
// sign with. The objects are addressed by an unguessable key instead: the asset
// id in it is random, so a URL cannot be found by trying. The alternative — a
// signed URL — would have to expire, and an expiring URL cannot be written to a
// session or a chat row without being dead by the time it is read back.
//
// A deployment with no store, or with an object store that serves its own
// bytes, answers 404: there is nothing here to serve, and saying so is more
// useful than a route that exists and always fails.
func (h *Handler) ServeMedia(c *gin.Context) {
	if h.media == nil {
		core.WriteResponse(c, nil, errno.ErrNotFound.WithMessage("no media store is configured"))
		return
	}
	h.media.ServeHTTP(c.Writer, c.Request)
}

// assetErrno maps an asset error to the response it deserves.
//
// The distinction that matters is between "you sent something I will not
// store" and "I could not store it": the first is the caller's to fix and
// carries the reason, the second is not and must not dress up as one.
func assetErrno(err error) error {
	switch {
	case errors.Is(err, agentstore.ErrNotFound):
		return errno.ErrPageNotFound.WithMessage("no such asset")
	case errors.Is(err, asset.ErrNotConfigured):
		// A 503 naming the missing configuration: nothing in the request can fix
		// an unset driver, and the operator reading the response is who needs to
		// know. The code is on the wire, so a caller need not read the message to
		// tell this from a store that failed.
		return errno.ErrNotConfigured.WithMessage("%s", err.Error())
	case errors.Is(err, asset.ErrEmptyData):
		return errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	case errors.Is(err, media.ErrUnsupportedKind),
		errors.Is(err, media.ErrUnsupportedType),
		errors.Is(err, media.ErrTooLarge),
		errors.Is(err, media.ErrInvalidObjectKey):
		return errno.ErrInvalidArgument.WithMessage("%s", err.Error())
	default:
		return errno.ErrInternal.WithMessage("%s", err.Error())
	}
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		authed := engine.Group("", mws...)
		authed.POST("/assets", h.CreateAsset)
		authed.GET("/assets/:assetID", h.GetAsset)
		authed.GET("/assets/:assetID/content", h.GetAssetContent)
		authed.DELETE("/assets/:assetID", h.DeleteAsset)
	})
}
