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

	"github.com/onexstack/onexai/internal/agent/biz/v1/imagegen"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// GenerateImages handles POST /v1/images/generations — the OpenAI-compatible
// image endpoint.
//
// It draws the images and stores them, so what a caller receives is a link this
// service will still serve tomorrow rather than a provider's temporary URL. See
// GeneratedImage for why that matters.
func (h *Handler) GenerateImages(c *gin.Context) {
	if h.biz == nil || h.biz.ImageGen() == nil || !h.biz.ImageGen().Configured() {
		// 503, not 500: the capability is absent, which a caller can tell from a
		// provider that failed without reading the message.
		core.WriteResponse(c, nil, errno.ErrNotConfigured.WithMessage("%s", imagegen.ErrNotConfigured.Error()))
		return
	}

	var req v1.ImageGenerationRequest
	if err := core.ShouldBindJSON(c, &req, h.val.ValidateImageGenerationRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	out, err := h.biz.ImageGen().Generate(c.Request.Context(), &req)
	if err != nil {
		// Every generation failure is reported as internal, with the cause
		// attached, rather than as a bad request. The failures come from the
		// provider — a rejected prompt, a quota, a missing model — and none of
		// them is something the caller can fix by changing their request, so
		// dressing one up as a 400 would send them editing a prompt that was
		// fine.
		core.WriteResponse(c, nil, errno.ErrInternal.WithMessage("%s", err.Error()))
		return
	}
	core.WriteResponse(c, out, nil)
}

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		authed := engine.Group("", mws...)
		authed.POST("/images/generations", h.GenerateImages)
	})
}
