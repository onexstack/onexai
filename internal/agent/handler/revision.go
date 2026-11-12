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
)

// ListAgentRevision handles GET /v1/agent/revisions?agent=&limit=.
//
// A revision is compiled when a run first needs it, so this endpoint answers
// "which versions of this agent have actually executed". An agent with no
// revisions has never run — it does not mean the agent is broken.
func (h *Handler) ListAgentRevision(c *gin.Context) {
	core.HandleRequest(c, c.ShouldBindQuery, h.biz.RunV1().ListRevision, h.val.ValidateListRevisionRequest)
}

// GetAgentRevision handles GET /v1/agent/revisions/:id.
func (h *Handler) GetAgentRevision(c *gin.Context) {
	core.HandleRequest(c, c.ShouldBindUri, h.biz.RunV1().GetRevision, h.val.ValidateGetRevisionRequest)
}

func init() { //nolint:gochecknoinits
	Register(func(v1 *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		v1.GET("/agent/revisions", h.ListAgentRevision)
		v1.GET("/agent/revisions/:id", h.GetAgentRevision)
	})
}
