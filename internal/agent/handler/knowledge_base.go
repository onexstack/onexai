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

// CreateKnowledgeBase handles POST /v1/agent/knowledgebases.
func (h *Handler) CreateKnowledgeBase(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.KnowledgeBaseV1().Create, h.val.ValidateCreateKnowledgeBaseRequest)
}

// GetKnowledgeBase handles GET /v1/agent/knowledgebases/:name.
func (h *Handler) GetKnowledgeBase(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.KnowledgeBaseV1().Get, h.val.ValidateGetKnowledgeBaseRequest)
}

// ListKnowledgeBase handles GET /v1/agent/knowledgebases.
func (h *Handler) ListKnowledgeBase(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.KnowledgeBaseV1().List, h.val.ValidateListKnowledgeBaseRequest)
}

// UpdateKnowledgeBase handles PUT /v1/agent/knowledgebases/:name.
func (h *Handler) UpdateKnowledgeBase(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateKnowledgeBaseRequest) {
		if rq.KnowledgeBase != nil {
			rq.KnowledgeBase.Name = c.Param("name")
		}
	}), h.biz.KnowledgeBaseV1().Update, h.val.ValidateUpdateKnowledgeBaseRequest)
}

// DeleteKnowledgeBase handles DELETE /v1/agent/knowledgebases/:name.
func (h *Handler) DeleteKnowledgeBase(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.KnowledgeBaseV1().Delete, h.val.ValidateDeleteKnowledgeBaseRequest)
}

// TestKnowledgeBaseRetrieval handles
// POST /v1/agent/knowledgebases/:name/retrieval-test: one retrieval against a
// stored record, with per-request overrides, without persisting anything.
//
// The name comes from the path, not the body, so the record tested is the one
// the console is looking at. A platform failure is 200 with ok=false — see
// TestRetrieval.
func (h *Handler) TestKnowledgeBaseRetrieval(c *gin.Context) {
	core.HandleAllRequest(c, h.biz.KnowledgeBaseV1().TestRetrieval, h.val.ValidateTestKnowledgeBaseRetrievalRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		kbs := engine.Group("/agent/knowledgebases")
		kbs.POST("", h.CreateKnowledgeBase)
		kbs.GET("", h.ListKnowledgeBase)
		kbs.GET("/:name", h.GetKnowledgeBase)
		kbs.PUT("/:name", h.UpdateKnowledgeBase)
		kbs.DELETE("/:name", h.DeleteKnowledgeBase)
		// A record-scoped action, like agents/:name/invoke: the test always
		// names a stored row, so the name belongs in the path.
		kbs.POST("/:name/retrieval-test", h.TestKnowledgeBaseRetrieval)
	})
}
