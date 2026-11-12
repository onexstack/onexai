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

package dutyservice

import (
	"context"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/duty"
)

func (s *service) scheduleBiz() (dutyScheduleBiz, error) {
	if s.biz == nil {
		return nil, duty.ErrScheduleUnavailable
	}
	return s.biz, nil
}

// dutyScheduleBiz is the slice of dutyschedule.DutyScheduleBiz this package
// calls. Keeping it named locally makes the delegation below readable.
type dutyScheduleBiz interface {
	Get(ctx context.Context, rq *api.GetOncallScheduleRequest) (*api.OncallScheduleResponse, error)
	SaveDefinition(
		ctx context.Context, rq *api.SaveOncallScheduleRequest,
	) (*api.OncallScheduleResponse, error)
	Preview(
		ctx context.Context, rq *api.PreviewOncallScheduleRequest,
	) (*api.OncallTimelineResponse, error)
	Current(ctx context.Context, rq *api.GetOncallCurrentRequest) (*api.OncallCurrentResponse, error)
	ListCurrent(
		ctx context.Context, rq *api.ListOncallCurrentRequest,
	) (*api.ListOncallCurrentResponse, error)
	Timeline(ctx context.Context, rq *api.GetOncallTimelineRequest) (*api.OncallTimelineResponse, error)
	LayerTimelines(
		ctx context.Context, rq *api.GetOncallLayerTimelinesRequest,
	) (*api.OncallLayerTimelinesResponse, error)
	ListOverrides(ctx context.Context, rq *api.ListOncallOverridesRequest) (*api.OverrideListResponse, error)
	CreateOverride(
		ctx context.Context, rq *api.CreateOncallOverrideRequest,
	) (*api.OncallScheduleOverride, error)
	DeleteOverride(ctx context.Context, rq *api.DeleteOncallOverrideRequest) (any, error)
	ListUnavailabilities(
		ctx context.Context, rq *api.ListOncallUnavailabilitiesRequest,
	) (*api.UnavailabilityListResponse, error)
	CreateUnavailability(
		ctx context.Context, rq *api.CreateOncallUnavailabilityRequest,
	) (*api.OncallScheduleUnavailability, error)
	DeleteUnavailability(ctx context.Context, rq *api.DeleteOncallUnavailabilityRequest) (any, error)
	ListHolidays(ctx context.Context, rq *api.ListOncallHolidaysRequest) (*api.HolidayListResponse, error)
	CreateHoliday(
		ctx context.Context, rq *api.CreateOncallHolidayRequest,
	) (*api.OncallScheduleHoliday, error)
	DeleteHoliday(ctx context.Context, rq *api.DeleteOncallHolidayRequest) (any, error)
	GetEscalation(
		ctx context.Context, rq *api.GetOncallEscalationRequest,
	) (*api.OncallEscalationResponse, error)
	SaveEscalation(
		ctx context.Context, rq *api.SaveOncallEscalationRequest,
	) (*api.OncallEscalationResponse, error)
	Escalate(ctx context.Context, rq *api.EscalateRequest) (*api.EscalateResponse, error)
}

// GetSchedule implements duty.ScheduleService.
func (s *service) GetSchedule(ctx context.Context, group string) (*api.OncallScheduleResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.Get(ctx, &api.GetOncallScheduleRequest{Group: group})
}

// SaveSchedule implements duty.ScheduleService.
func (s *service) SaveSchedule(
	ctx context.Context, group string, def *api.OncallScheduleDefinition,
) (*api.OncallScheduleResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, duty.ErrScheduleUnavailable
	}
	return biz.SaveDefinition(ctx, &api.SaveOncallScheduleRequest{
		Group:                    group,
		OncallScheduleDefinition: *def,
	})
}

// PreviewSchedule implements duty.ScheduleService.
func (s *service) PreviewSchedule(
	ctx context.Context, group string, def *api.OncallScheduleDefinition, from, to time.Time,
) (*api.OncallTimelineResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, duty.ErrScheduleUnavailable
	}
	return biz.Preview(ctx, &api.PreviewOncallScheduleRequest{
		Group:                    group,
		From:                     from,
		To:                       to,
		OncallScheduleDefinition: *def,
	})
}

// GetCurrent implements duty.ScheduleService.
func (s *service) GetCurrent(
	ctx context.Context, group string, at time.Time,
) (*api.OncallCurrentResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.Current(ctx, &api.GetOncallCurrentRequest{Group: group, AtTime: at})
}

// ListCurrent implements duty.ScheduleService. The biz always evaluates at
// "now"; a caller that asked for a different instant is answered group by
// group so the result stays exact.
func (s *service) ListCurrent(
	ctx context.Context, names []string, at time.Time,
) (*api.ListOncallCurrentResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	if at.IsZero() {
		return biz.ListCurrent(ctx, &api.ListOncallCurrentRequest{NamesList: names})
	}
	return s.listCurrentAt(ctx, biz, names, at)
}

func (s *service) listCurrentAt(
	ctx context.Context, biz dutyScheduleBiz, names []string, at time.Time,
) (*api.ListOncallCurrentResponse, error) {
	groups, err := s.ListGroupInfos(ctx)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[strings.ToLower(name)] = struct{}{}
	}
	items := make([]*api.OncallCurrentResponse, 0, len(groups))
	for _, g := range groups {
		if len(wanted) > 0 {
			if _, ok := wanted[strings.ToLower(g.ID)]; !ok {
				if _, ok := wanted[strings.ToLower(g.Name)]; !ok {
					continue
				}
			}
		}
		var item *api.OncallCurrentResponse
		if g.ScheduleSource == "native" {
			// The schedule store keys on the stored name, never on the display
			// name or the lower-cased id.
			groupKey := g.RawName
			if groupKey == "" {
				groupKey = g.ID
			}
			item, err = biz.Current(ctx, &api.GetOncallCurrentRequest{Group: groupKey, AtTime: at})
			if err != nil {
				return nil, err
			}
		} else {
			item = currentFromGroup(g, at)
		}
		items = append(items, item)
	}
	return &api.ListOncallCurrentResponse{Items: items}, nil
}

// currentFromGroup renders a non-native group's roster at the requested
// instant. A legacy roster has no durable since/until or layer, so only the
// names are carried over.
func currentFromGroup(g duty.Group, at time.Time) *api.OncallCurrentResponse {
	group := g.RawName
	if group == "" {
		group = g.ID
	}
	resp := &api.OncallCurrentResponse{Group: group, At: at, Source: g.ScheduleSource}
	if len(g.Watchers) == 0 {
		return resp
	}
	resp.Current = &api.OncallCurrentMember{
		UserName:   g.Watchers[0].UserName,
		IsOverride: g.PrimaryIsOverride,
		LayerName:  g.PrimaryLayerName,
		Until:      g.PrimaryUntil,
	}
	for _, watcher := range g.Watchers[1:] {
		resp.Backups = append(resp.Backups, api.OncallCurrentMember{UserName: watcher.UserName})
	}
	return resp
}

// GetTimeline implements duty.ScheduleService.
func (s *service) GetTimeline(
	ctx context.Context, group string, from, to time.Time,
) (*api.OncallTimelineResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.Timeline(ctx, &api.GetOncallTimelineRequest{Group: group, FromTime: from, ToTime: to})
}

// GetLayerTimelines implements duty.ScheduleService.
func (s *service) GetLayerTimelines(
	ctx context.Context, group string, from, to time.Time,
) (*api.OncallLayerTimelinesResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.LayerTimelines(ctx, &api.GetOncallLayerTimelinesRequest{
		Group: group, FromTime: from, ToTime: to,
	})
}

// ListOverrides implements duty.ScheduleService.
func (s *service) ListOverrides(ctx context.Context, group string) (*api.OverrideListResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.ListOverrides(ctx, &api.ListOncallOverridesRequest{Group: group})
}

// CreateOverride implements duty.ScheduleService.
func (s *service) CreateOverride(
	ctx context.Context, group, user string, start, end time.Time, createdBy string,
) (*api.OncallScheduleOverride, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.CreateOverride(ctx, &api.CreateOncallOverrideRequest{
		Group:        group,
		OverrideUser: user,
		StartTime:    start,
		EndTime:      end,
		CreatedBy:    createdBy,
	})
}

// DeleteOverride implements duty.ScheduleService.
func (s *service) DeleteOverride(ctx context.Context, group, id string) error {
	biz, err := s.scheduleBiz()
	if err != nil {
		return err
	}
	_, err = biz.DeleteOverride(ctx, &api.DeleteOncallOverrideRequest{Group: group, OverrideID: id})
	return err
}

// ListUnavailabilities implements duty.ScheduleService.
func (s *service) ListUnavailabilities(
	ctx context.Context, group string,
) (*api.UnavailabilityListResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.ListUnavailabilities(ctx, &api.ListOncallUnavailabilitiesRequest{Group: group})
}

// CreateUnavailability implements duty.ScheduleService.
func (s *service) CreateUnavailability(
	ctx context.Context, group, user, startDate, endDate, reason, createdBy string,
) (*api.OncallScheduleUnavailability, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.CreateUnavailability(ctx, &api.CreateOncallUnavailabilityRequest{
		Group:     group,
		UserName:  user,
		StartDate: startDate,
		EndDate:   endDate,
		Reason:    reason,
		CreatedBy: createdBy,
	})
}

// DeleteUnavailability implements duty.ScheduleService.
func (s *service) DeleteUnavailability(ctx context.Context, group, id string) error {
	biz, err := s.scheduleBiz()
	if err != nil {
		return err
	}
	_, err = biz.DeleteUnavailability(ctx, &api.DeleteOncallUnavailabilityRequest{
		Group: group, UnavailabilityID: id,
	})
	return err
}

// ListHolidays implements duty.ScheduleService.
func (s *service) ListHolidays(ctx context.Context, group string) (*api.HolidayListResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.ListHolidays(ctx, &api.ListOncallHolidaysRequest{Group: group})
}

// CreateHoliday implements duty.ScheduleService.
func (s *service) CreateHoliday(
	ctx context.Context, group, date, name, country string,
) (*api.OncallScheduleHoliday, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.CreateHoliday(ctx, &api.CreateOncallHolidayRequest{
		Group: group, Date: date, Name: name, CountryCode: country,
	})
}

// DeleteHoliday implements duty.ScheduleService.
func (s *service) DeleteHoliday(ctx context.Context, group, id string) error {
	biz, err := s.scheduleBiz()
	if err != nil {
		return err
	}
	_, err = biz.DeleteHoliday(ctx, &api.DeleteOncallHolidayRequest{Group: group, HolidayID: id})
	return err
}

// GetEscalation implements duty.ScheduleService.
func (s *service) GetEscalation(
	ctx context.Context, group string,
) (*api.OncallEscalationResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.GetEscalation(ctx, &api.GetOncallEscalationRequest{Group: group})
}

// SaveEscalation implements duty.ScheduleService.
func (s *service) SaveEscalation(
	ctx context.Context, group string, policy *api.OncallEscalationPolicy,
) (*api.OncallEscalationResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	if policy == nil {
		return nil, duty.ErrScheduleUnavailable
	}
	return biz.SaveEscalation(ctx, &api.SaveOncallEscalationRequest{
		Group:                  group,
		OncallEscalationPolicy: *policy,
	})
}

// Escalate implements duty.ScheduleService.
func (s *service) Escalate(
	ctx context.Context, group, ticketID string,
) (*api.EscalateResponse, error) {
	biz, err := s.scheduleBiz()
	if err != nil {
		return nil, err
	}
	return biz.Escalate(ctx, &api.EscalateRequest{Group: group, TicketID: ticketID})
}
