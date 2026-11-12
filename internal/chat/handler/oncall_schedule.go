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

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/restbind"
)

func init() {
	Register(func(engine *gin.RouterGroup, h *Handler, _ ...gin.HandlerFunc) {
		reads := engine.Group("/oncall-groups", h.AuthMiddleware())
		writes := reads.Group("", bearer.RequireAdmin())

		reads.GET("/:name/schedule", h.GetOncallSchedule)
		reads.GET("/:name/schedule/oncall", h.GetOncallCurrent)
		reads.GET("/:name/schedule/timeline", h.GetOncallTimeline)
		reads.GET("/:name/schedule/layer-timelines", h.GetOncallLayerTimelines)
		reads.GET("/:name/schedule/overrides", h.ListOncallOverride)
		reads.GET("/:name/schedule/unavailabilities", h.ListOncallUnavailability)
		reads.GET("/:name/schedule/holidays", h.ListOncallHoliday)
		reads.GET("/:name/escalation", h.GetOncallEscalation)

		writes.PUT("/:name/schedule", h.SaveOncallSchedule)
		writes.POST("/:name/schedule/preview", h.PreviewOncallSchedule)
		writes.POST("/:name/schedule/overrides", h.CreateOncallOverride)
		writes.DELETE("/:name/schedule/overrides/:overrideID", h.DeleteOncallOverride)
		writes.POST("/:name/schedule/unavailabilities", h.CreateOncallUnavailability)
		writes.DELETE("/:name/schedule/unavailabilities/:unavailabilityID", h.DeleteOncallUnavailability)
		writes.POST("/:name/schedule/holidays", h.CreateOncallHoliday)
		writes.DELETE("/:name/schedule/holidays/:holidayID", h.DeleteOncallHoliday)
		writes.PUT("/:name/escalation", h.SaveOncallEscalation)
		writes.POST("/:name/escalate", h.EscalateTicket)

		// A top-level route rather than /oncall-groups/current: a static path
		// below /:name would collide with the group route in gin.
		engine.GET("/oncall-current", h.AuthMiddleware(), h.ListOncallCurrent)
	})
}

// GetOncallSchedule handles GET /v1/oncall-groups/:name/schedule.
func (h *Handler) GetOncallSchedule(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[api.GetOncallScheduleRequest](c),
		h.biz.DutyScheduleV1().Get, h.val.ValidateGetOncallScheduleRequest)
}

// SaveOncallSchedule handles PUT /v1/oncall-groups/:name/schedule.
func (h *Handler) SaveOncallSchedule(c *gin.Context) {
	core.HandleAllRequest(c, h.biz.DutyScheduleV1().SaveDefinition, h.val.ValidateSaveOncallScheduleRequest)
}

// PreviewOncallSchedule handles POST /v1/oncall-groups/:name/schedule/preview.
func (h *Handler) PreviewOncallSchedule(c *gin.Context) {
	core.HandleAllRequest(c, h.biz.DutyScheduleV1().Preview, h.val.ValidatePreviewOncallScheduleRequest)
}

// GetOncallCurrent handles GET /v1/oncall-groups/:name/schedule/oncall.
func (h *Handler) GetOncallCurrent(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[api.GetOncallCurrentRequest](c),
		h.biz.DutyScheduleV1().Current, h.val.ValidateGetOncallCurrentRequest)
}

// ListOncallCurrent handles GET /v1/oncall-current.
func (h *Handler) ListOncallCurrent(c *gin.Context) {
	core.HandleRequest(c, c.ShouldBindQuery,
		h.biz.DutyScheduleV1().ListCurrent, h.val.ValidateListOncallCurrentRequest)
}

// GetOncallTimeline handles GET /v1/oncall-groups/:name/schedule/timeline.
func (h *Handler) GetOncallTimeline(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[api.GetOncallTimelineRequest](c),
		h.biz.DutyScheduleV1().Timeline, h.val.ValidateGetOncallTimelineRequest)
}

// GetOncallLayerTimelines handles GET /v1/oncall-groups/:name/schedule/layer-timelines.
func (h *Handler) GetOncallLayerTimelines(c *gin.Context) {
	core.HandleRequest(c, restbind.URIAndQuery[api.GetOncallLayerTimelinesRequest](c),
		h.biz.DutyScheduleV1().LayerTimelines, h.val.ValidateGetOncallLayerTimelinesRequest)
}

// ListOncallOverride handles GET /v1/oncall-groups/:name/schedule/overrides.
func (h *Handler) ListOncallOverride(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.DutyScheduleV1().ListOverrides, h.val.ValidateListOncallOverridesRequest)
}

// CreateOncallOverride handles POST /v1/oncall-groups/:name/schedule/overrides.
func (h *Handler) CreateOncallOverride(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *api.CreateOncallOverrideRequest) {
		rq.Group = c.Param("name")
		rq.CreatedBy = callerName(c)
	}), h.biz.DutyScheduleV1().CreateOverride, h.val.ValidateCreateOncallOverrideRequest)
}

// DeleteOncallOverride handles DELETE /v1/oncall-groups/:name/schedule/overrides/:overrideID.
func (h *Handler) DeleteOncallOverride(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.DutyScheduleV1().DeleteOverride, h.val.ValidateDeleteOncallOverrideRequest)
}

// ListOncallUnavailability handles GET /v1/oncall-groups/:name/schedule/unavailabilities.
func (h *Handler) ListOncallUnavailability(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.DutyScheduleV1().ListUnavailabilities, h.val.ValidateListOncallUnavailabilitiesRequest)
}

// CreateOncallUnavailability handles POST /v1/oncall-groups/:name/schedule/unavailabilities.
func (h *Handler) CreateOncallUnavailability(c *gin.Context) {
	core.HandleRequest(c, restbind.JSONWithPath(c, func(rq *api.CreateOncallUnavailabilityRequest) {
		rq.Group = c.Param("name")
		rq.CreatedBy = callerName(c)
	}), h.biz.DutyScheduleV1().CreateUnavailability, h.val.ValidateCreateOncallUnavailabilityRequest)
}

// DeleteOncallUnavailability handles DELETE /v1/oncall-groups/:name/schedule/unavailabilities/:unavailabilityID.
func (h *Handler) DeleteOncallUnavailability(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.DutyScheduleV1().DeleteUnavailability, h.val.ValidateDeleteOncallUnavailabilityRequest)
}

// ListOncallHoliday handles GET /v1/oncall-groups/:name/schedule/holidays.
func (h *Handler) ListOncallHoliday(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.DutyScheduleV1().ListHolidays, h.val.ValidateListOncallHolidaysRequest)
}

// CreateOncallHoliday handles POST /v1/oncall-groups/:name/schedule/holidays.
func (h *Handler) CreateOncallHoliday(c *gin.Context) {
	core.HandleAllRequest(c, h.biz.DutyScheduleV1().CreateHoliday, h.val.ValidateCreateOncallHolidayRequest)
}

// DeleteOncallHoliday handles DELETE /v1/oncall-groups/:name/schedule/holidays/:holidayID.
func (h *Handler) DeleteOncallHoliday(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.DutyScheduleV1().DeleteHoliday, h.val.ValidateDeleteOncallHolidayRequest)
}

// GetOncallEscalation handles GET /v1/oncall-groups/:name/escalation.
func (h *Handler) GetOncallEscalation(c *gin.Context) {
	core.HandleUriRequest(c, h.biz.DutyScheduleV1().GetEscalation, h.val.ValidateGetOncallEscalationRequest)
}

// SaveOncallEscalation handles PUT /v1/oncall-groups/:name/escalation.
func (h *Handler) SaveOncallEscalation(c *gin.Context) {
	core.HandleAllRequest(c, h.biz.DutyScheduleV1().SaveEscalation, h.val.ValidateSaveOncallEscalationRequest)
}

// EscalateTicket handles POST /v1/oncall-groups/:name/escalate.
func (h *Handler) EscalateTicket(c *gin.Context) {
	core.HandleAllRequest(c, h.biz.DutyScheduleV1().Escalate, h.val.ValidateEscalateRequest)
}

// callerName is the audit name of the authenticated caller. The display name is
// preferred; a token minted before it existed falls back to the user id.
func callerName(c *gin.Context) string {
	if claims, ok := bearer.ClaimsFrom(c); ok {
		if claims.Name != "" {
			return claims.Name
		}
		return claims.UserID
	}
	return ""
}
