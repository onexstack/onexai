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

// CreateAuthzRule handles POST /v1/agent/authorizationrules.
func (h *Handler) CreateAuthzRule(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.AuthzRuleV1().Create, h.val.ValidateCreateAuthzRuleRequest)
}

// GetAuthzRule handles GET /v1/agent/authorizationrules/:name.
func (h *Handler) GetAuthzRule(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AuthzRuleV1().Get, h.val.ValidateGetAuthzRuleRequest)
}

// ListAuthzRule handles GET /v1/agent/authorizationrules.
func (h *Handler) ListAuthzRule(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.AuthzRuleV1().List, h.val.ValidateListAuthzRuleRequest)
}

// UpdateAuthzRule handles PUT /v1/agent/authorizationrules/:name. The path names the rule;
// a name in the body is ignored, as it is for every other resource endpoint.
func (h *Handler) UpdateAuthzRule(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *v1.UpdateAuthzRuleRequest) {
		if rq.AuthzRule != nil {
			rq.AuthzRule.Name = c.Param("name")
		}
	}), h.biz.AuthzRuleV1().Update, h.val.ValidateUpdateAuthzRuleRequest)
}

// DeleteAuthzRule handles DELETE /v1/agent/authorizationrules/:name.
func (h *Handler) DeleteAuthzRule(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.AuthzRuleV1().Delete, h.val.ValidateDeleteAuthzRuleRequest)
}

func init() {
	RegisterUnified(func(engine *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc) {
		rules := engine.Group("/agent/authorizationrules")
		rules.POST("", h.CreateAuthzRule)
		rules.GET("", h.ListAuthzRule)
		rules.GET("/:name", h.GetAuthzRule)
		rules.PUT("/:name", h.UpdateAuthzRule)
		rules.DELETE("/:name", h.DeleteAuthzRule)
	})
}
