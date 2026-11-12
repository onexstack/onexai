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

// CreateSkill handles POST /v1/agent/skills.
func (h *Handler) CreateSkill(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.SkillV1().Create, h.val.ValidateCreateSkillRequest)
}

// GetSkill handles GET /v1/agent/skills/:name.
func (h *Handler) GetSkill(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.SkillV1().Get, h.val.ValidateGetSkillRequest)
}

// ListSkill handles GET /v1/agent/skills.
func (h *Handler) ListSkill(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.SkillV1().List, h.val.ValidateListSkillRequest)
}

// UpdateSkill handles PUT /v1/agent/skills/:name.
func (h *Handler) UpdateSkill(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateSkillRequest) {
		if rq.GetSkill() != nil {
			rq.Skill.Name = c.Param("name") //nolint:protogetter // writing the request field
		}
	}), h.biz.SkillV1().Update, h.val.ValidateUpdateSkillRequest)
}

// DeleteSkill handles DELETE /v1/agent/skills/:name.
func (h *Handler) DeleteSkill(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.SkillV1().Delete, h.val.ValidateDeleteSkillRequest)
}

// ListContextSkill handles GET /v1/agent/agents/:name/context-skills.
//
// It exists because a skill's name is not in the record the CRUD face serves:
// the record names a repository, and the skills inside it are only knowable by
// having the skill subsystem list them. A chat surface that wants to offer
// "/<skill>" as a command therefore has to ask, and this is where it asks.
func (h *Handler) ListContextSkill(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.SkillV1().ListContextSkill, h.val.ValidateGetAgentRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		skills := engine.Group("/agent/skills")
		skills.POST("", h.CreateSkill)
		skills.GET("", h.ListSkill)
		skills.GET("/:name", h.GetSkill)
		skills.PUT("/:name", h.UpdateSkill)
		skills.DELETE("/:name", h.DeleteSkill)
	})
}
