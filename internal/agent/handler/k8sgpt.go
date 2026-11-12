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
// The original repo for this file is https://github.com/onexstack/onex.

package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
)

// GetLogs handles POST /v1/observability/get-logs.
//
// It routes through the same Router the agent's tools use, so a request made
// over HTTP and the same request made by the model reach the identical cluster
// by the identical path.
func (h *Handler) GetLogs(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.ObservabilityV1().GetLogs, h.val.ValidateGetLogsRequest)
}

// ListK8sEvent handles POST /v1/observability/list-k8s-events.
func (h *Handler) ListK8sEvent(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.ObservabilityV1().ListEvent, h.val.ValidateListK8sEventRequest)
}

func init() { //nolint:gochecknoinits
	Register(func(v1Group *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		observability := v1Group.Group("/observability")
		observability.POST("/get-logs", h.GetLogs)
		observability.POST("/list-k8s-events", h.ListK8sEvent)
	})
}
