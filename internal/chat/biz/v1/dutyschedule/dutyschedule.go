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

// Package dutyschedule implements the native on-call schedule: rotation
// definitions, overrides, unavailabilities, holidays and escalation policy.
//
// The schedule is 1:1 with an on-call group and lives in SQL. A group that has
// not been migrated yet keeps its legacy Regen schedule id and is served by the
// duty store's provider fallback; this package reports that as source "regen".
package dutyschedule

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/chat/api"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/oncall"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/internal/chat/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/storeerr"
)

// ProviderSet defines Wire DI providers for the schedule biz layer.
var ProviderSet = wire.NewSet(New, wire.Bind(new(DutyScheduleBiz), new(*dutyScheduleBiz)))

// RosterRefresher is the narrow slice of the duty store the biz layer needs to
// make a schedule edit visible to /duty and ticket assignment immediately.
type RosterRefresher interface {
	Refresh(ctx context.Context) error
}

// LegacyRoster exposes the in-memory watchers of groups that still use a Regen
// schedule.
type LegacyRoster interface {
	LegacyWatchers(group string) ([]string, bool)
}

// TicketEscalator is implemented by the escalation engine. Keeping it an
// interface lets the REST layer work in a deployment that never enables the
// worker.
type TicketEscalator interface {
	EscalateNow(ctx context.Context, group, ticketID string) (*api.EscalateResponse, error)
}

// DutyScheduleBiz defines the native schedule business logic.
type DutyScheduleBiz interface {
	Get(ctx context.Context, rq *api.GetOncallScheduleRequest) (*api.OncallScheduleResponse, error)
	SaveDefinition(ctx context.Context, rq *api.SaveOncallScheduleRequest) (*api.OncallScheduleResponse, error)
	Preview(ctx context.Context, rq *api.PreviewOncallScheduleRequest) (*api.OncallTimelineResponse, error)
	Current(ctx context.Context, rq *api.GetOncallCurrentRequest) (*api.OncallCurrentResponse, error)
	ListCurrent(ctx context.Context, rq *api.ListOncallCurrentRequest) (*api.ListOncallCurrentResponse, error)
	Timeline(ctx context.Context, rq *api.GetOncallTimelineRequest) (*api.OncallTimelineResponse, error)
	LayerTimelines(ctx context.Context, rq *api.GetOncallLayerTimelinesRequest) (*api.OncallLayerTimelinesResponse, error)

	ListOverrides(ctx context.Context, rq *api.ListOncallOverridesRequest) (*api.OverrideListResponse, error)
	CreateOverride(ctx context.Context, rq *api.CreateOncallOverrideRequest) (*api.OncallScheduleOverride, error)
	DeleteOverride(ctx context.Context, rq *api.DeleteOncallOverrideRequest) (any, error)

	ListUnavailabilities(ctx context.Context, rq *api.ListOncallUnavailabilitiesRequest) (*api.UnavailabilityListResponse, error)
	CreateUnavailability(ctx context.Context, rq *api.CreateOncallUnavailabilityRequest) (*api.OncallScheduleUnavailability, error)
	DeleteUnavailability(ctx context.Context, rq *api.DeleteOncallUnavailabilityRequest) (any, error)

	ListHolidays(ctx context.Context, rq *api.ListOncallHolidaysRequest) (*api.HolidayListResponse, error)
	CreateHoliday(ctx context.Context, rq *api.CreateOncallHolidayRequest) (*api.OncallScheduleHoliday, error)
	DeleteHoliday(ctx context.Context, rq *api.DeleteOncallHolidayRequest) (any, error)

	GetEscalation(ctx context.Context, rq *api.GetOncallEscalationRequest) (*api.OncallEscalationResponse, error)
	SaveEscalation(ctx context.Context, rq *api.SaveOncallEscalationRequest) (*api.OncallEscalationResponse, error)
	Escalate(ctx context.Context, rq *api.EscalateRequest) (*api.EscalateResponse, error)
}

type dutyScheduleBiz struct {
	st        store.IStore
	legacy    LegacyRoster
	refresher RosterRefresher
	escalator TicketEscalator
}

var _ DutyScheduleBiz = (*dutyScheduleBiz)(nil)

// New constructs the schedule biz. legacy, refresher and escalator may be nil
// in tests and in a deployment without a duty store.
func New(
	st store.IStore, legacy LegacyRoster, refresher RosterRefresher, escalator TicketEscalator,
) *dutyScheduleBiz {
	return &dutyScheduleBiz{st: st, legacy: legacy, refresher: refresher, escalator: escalator}
}

func (b *dutyScheduleBiz) Get(
	ctx context.Context, rq *api.GetOncallScheduleRequest,
) (*api.OncallScheduleResponse, error) {
	group, err := b.group(ctx, rq.Group)
	if err != nil {
		return nil, err
	}
	agg, err := b.st.OncallSchedules().GetAggregate(ctx, rq.Group)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	return b.response(rq.Group, group, agg), nil
}

func (b *dutyScheduleBiz) SaveDefinition(
	ctx context.Context, rq *api.SaveOncallScheduleRequest,
) (*api.OncallScheduleResponse, error) {
	group, err := b.group(ctx, rq.Group)
	if err != nil {
		return nil, err
	}
	if err := b.st.OncallSchedules().SaveDefinition(ctx, definitionOf(rq.Group, &rq.OncallScheduleDefinition)); err != nil {
		return nil, storeerr.Write(err)
	}
	b.refreshRoster(ctx)

	agg, err := b.st.OncallSchedules().GetAggregate(ctx, rq.Group)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	return b.response(rq.Group, group, agg), nil
}

func (b *dutyScheduleBiz) Preview(
	ctx context.Context, rq *api.PreviewOncallScheduleRequest,
) (*api.OncallTimelineResponse, error) {
	if err := b.requireGroup(ctx, rq.Group); err != nil {
		return nil, err
	}
	def := definitionOf(rq.Group, &rq.OncallScheduleDefinition)

	agg := aggregateOfDefinition(def)
	if existing, err := b.st.OncallSchedules().GetAggregate(ctx, rq.Group); err != nil {
		return nil, storeerr.Read(err)
	} else if existing != nil {
		agg.Overrides = existing.Overrides
		agg.Unavailabilities = existing.Unavailabilities
		agg.Holidays = existing.Holidays
	}

	return &api.OncallTimelineResponse{
		Group:    rq.Group,
		From:     rq.From.UTC(),
		To:       rq.To.UTC(),
		Timezone: def.Schedule.Timezone,
		Segments: segmentsOf(oncall.Timeline(agg, rq.From, rq.To)),
	}, nil
}

func (b *dutyScheduleBiz) Current(
	ctx context.Context, rq *api.GetOncallCurrentRequest,
) (*api.OncallCurrentResponse, error) {
	group, err := b.group(ctx, rq.Group)
	if err != nil {
		return nil, err
	}
	agg, err := b.st.OncallSchedules().GetAggregate(ctx, rq.Group)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	return b.current(rq.Group, group, agg, rq.AtTime), nil
}

func (b *dutyScheduleBiz) ListCurrent(
	ctx context.Context, rq *api.ListOncallCurrentRequest,
) (*api.ListOncallCurrentResponse, error) {
	groups, err := b.st.OncallGroups().ListAll(ctx)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	aggs, err := b.st.OncallSchedules().ListAggregates(ctx)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	byGroup := make(map[string]*model.OncallScheduleAggregate, len(aggs))
	for _, agg := range aggs {
		if agg != nil && agg.Schedule != nil {
			byGroup[agg.Schedule.GroupName] = agg
		}
	}

	wanted := make(map[string]struct{}, len(rq.NamesList))
	for _, name := range rq.NamesList {
		wanted[strings.ToLower(name)] = struct{}{}
	}

	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Order == groups[j].Order {
			return groups[i].Name < groups[j].Name
		}
		return groups[i].Order < groups[j].Order
	})

	at := time.Now().UTC()
	items := make([]*api.OncallCurrentResponse, 0, len(groups))
	for _, group := range groups {
		if group == nil {
			continue
		}
		if len(wanted) > 0 {
			if _, ok := wanted[strings.ToLower(group.Name)]; !ok {
				continue
			}
		}
		items = append(items, b.current(group.Name, group, byGroup[group.Name], at))
	}
	return &api.ListOncallCurrentResponse{Items: items}, nil
}

func (b *dutyScheduleBiz) Timeline(
	ctx context.Context, rq *api.GetOncallTimelineRequest,
) (*api.OncallTimelineResponse, error) {
	agg, err := b.nativeAggregate(ctx, rq.Group)
	if err != nil {
		return nil, err
	}
	return &api.OncallTimelineResponse{
		Group:    rq.Group,
		From:     rq.FromTime,
		To:       rq.ToTime,
		Timezone: agg.Schedule.Timezone,
		Segments: segmentsOf(oncall.Timeline(agg, rq.FromTime, rq.ToTime)),
	}, nil
}

func (b *dutyScheduleBiz) LayerTimelines(
	ctx context.Context, rq *api.GetOncallLayerTimelinesRequest,
) (*api.OncallLayerTimelinesResponse, error) {
	agg, err := b.nativeAggregate(ctx, rq.Group)
	if err != nil {
		return nil, err
	}
	layers, effective := oncall.LayerTimelines(agg, rq.FromTime, rq.ToTime)
	outLayers := make(map[string][]api.OncallSegment, len(layers))
	for name, segments := range layers {
		outLayers[name] = segmentsOf(segments)
	}
	return &api.OncallLayerTimelinesResponse{
		Group:     rq.Group,
		From:      rq.FromTime,
		To:        rq.ToTime,
		Timezone:  agg.Schedule.Timezone,
		Layers:    outLayers,
		Effective: segmentsOf(effective),
	}, nil
}

func (b *dutyScheduleBiz) ListOverrides(
	ctx context.Context, rq *api.ListOncallOverridesRequest,
) (*api.OverrideListResponse, error) {
	if err := b.requireGroup(ctx, rq.Group); err != nil {
		return nil, err
	}
	rows, err := b.st.OncallSchedules().ListOverrides(ctx, rq.Group)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	out := make([]api.OncallScheduleOverride, 0, len(rows))
	for _, row := range rows {
		out = append(out, overrideOf(row))
	}
	return &api.OverrideListResponse{Overrides: out}, nil
}

func (b *dutyScheduleBiz) CreateOverride(
	ctx context.Context, rq *api.CreateOncallOverrideRequest,
) (*api.OncallScheduleOverride, error) {
	if _, err := b.nativeAggregate(ctx, rq.Group); err != nil {
		return nil, err
	}
	row := &model.OncallScheduleOverrideM{
		GroupName:    rq.Group,
		OverrideUser: rq.OverrideUser,
		StartTime:    rq.StartTime.UTC(),
		EndTime:      rq.EndTime.UTC(),
		CreatedBy:    rq.CreatedBy,
		CreatedAt:    time.Now().UnixMilli(),
	}
	if err := b.st.OncallSchedules().CreateOverride(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	b.refreshRoster(ctx)
	out := overrideOf(row)
	return &out, nil
}

func (b *dutyScheduleBiz) DeleteOverride(
	ctx context.Context, rq *api.DeleteOncallOverrideRequest,
) (any, error) {
	if err := b.st.OncallSchedules().DeleteOverride(ctx, rq.Group, rq.OverrideID); err != nil {
		if errors.Is(err, store.ErrRecordNotFound) {
			return nil, errno.ErrNotFound.WithMessage("override %q not found", rq.OverrideID)
		}
		return nil, storeerr.Write(err)
	}
	b.refreshRoster(ctx)
	return nil, nil
}

func (b *dutyScheduleBiz) ListUnavailabilities(
	ctx context.Context, rq *api.ListOncallUnavailabilitiesRequest,
) (*api.UnavailabilityListResponse, error) {
	if err := b.requireGroup(ctx, rq.Group); err != nil {
		return nil, err
	}
	rows, err := b.st.OncallSchedules().ListUnavailabilities(ctx, rq.Group)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	out := make([]api.OncallScheduleUnavailability, 0, len(rows))
	for _, row := range rows {
		out = append(out, unavailabilityOf(row))
	}
	return &api.UnavailabilityListResponse{Unavailabilities: out}, nil
}

func (b *dutyScheduleBiz) CreateUnavailability(
	ctx context.Context, rq *api.CreateOncallUnavailabilityRequest,
) (*api.OncallScheduleUnavailability, error) {
	if _, err := b.nativeAggregate(ctx, rq.Group); err != nil {
		return nil, err
	}
	row := &model.OncallScheduleUnavailabilityM{
		GroupName: rq.Group,
		UserName:  rq.UserName,
		StartDate: rq.StartDate,
		EndDate:   rq.EndDate,
		Reason:    rq.Reason,
		CreatedBy: rq.CreatedBy,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := b.st.OncallSchedules().CreateUnavailability(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	b.refreshRoster(ctx)
	out := unavailabilityOf(row)
	return &out, nil
}

func (b *dutyScheduleBiz) DeleteUnavailability(
	ctx context.Context, rq *api.DeleteOncallUnavailabilityRequest,
) (any, error) {
	if err := b.st.OncallSchedules().DeleteUnavailability(ctx, rq.Group, rq.UnavailabilityID); err != nil {
		if errors.Is(err, store.ErrRecordNotFound) {
			return nil, errno.ErrNotFound.WithMessage("unavailability %q not found", rq.UnavailabilityID)
		}
		return nil, storeerr.Write(err)
	}
	b.refreshRoster(ctx)
	return nil, nil
}

func (b *dutyScheduleBiz) ListHolidays(
	ctx context.Context, rq *api.ListOncallHolidaysRequest,
) (*api.HolidayListResponse, error) {
	if err := b.requireGroup(ctx, rq.Group); err != nil {
		return nil, err
	}
	rows, err := b.st.OncallSchedules().ListHolidays(ctx, rq.Group)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	out := make([]api.OncallScheduleHoliday, 0, len(rows))
	for _, row := range rows {
		out = append(out, holidayOf(row))
	}
	return &api.HolidayListResponse{Holidays: out}, nil
}

func (b *dutyScheduleBiz) CreateHoliday(
	ctx context.Context, rq *api.CreateOncallHolidayRequest,
) (*api.OncallScheduleHoliday, error) {
	if _, err := b.nativeAggregate(ctx, rq.Group); err != nil {
		return nil, err
	}
	row := &model.OncallScheduleHolidayM{
		GroupName:   rq.Group,
		Date:        rq.Date,
		Name:        rq.Name,
		CountryCode: rq.CountryCode,
		CreatedAt:   time.Now().UnixMilli(),
	}
	if err := b.st.OncallSchedules().CreateHoliday(ctx, row); err != nil {
		return nil, storeerr.Write(err)
	}
	out := holidayOf(row)
	return &out, nil
}

func (b *dutyScheduleBiz) DeleteHoliday(
	ctx context.Context, rq *api.DeleteOncallHolidayRequest,
) (any, error) {
	if err := b.st.OncallSchedules().DeleteHoliday(ctx, rq.Group, rq.HolidayID); err != nil {
		if errors.Is(err, store.ErrRecordNotFound) {
			return nil, errno.ErrNotFound.WithMessage("holiday %q not found", rq.HolidayID)
		}
		return nil, storeerr.Write(err)
	}
	return nil, nil
}

func (b *dutyScheduleBiz) GetEscalation(
	ctx context.Context, rq *api.GetOncallEscalationRequest,
) (*api.OncallEscalationResponse, error) {
	if err := b.requireGroup(ctx, rq.Group); err != nil {
		return nil, err
	}
	agg, err := b.st.OncallSchedules().GetAggregate(ctx, rq.Group)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	resp := &api.OncallEscalationResponse{Group: rq.Group}
	if agg == nil || agg.Schedule == nil {
		return resp, nil
	}
	resp.Configured = true
	resp.Policy = policyOf(agg)
	return resp, nil
}

func (b *dutyScheduleBiz) SaveEscalation(
	ctx context.Context, rq *api.SaveOncallEscalationRequest,
) (*api.OncallEscalationResponse, error) {
	agg, err := b.nativeAggregate(ctx, rq.Group)
	if err != nil {
		return nil, err
	}
	schedule := *agg.Schedule
	schedule.EscalationEnabled = rq.Enabled
	schedule.EscalationAuto = rq.AutoEscalate
	schedule.EscalationFromLevel = rq.AutoEscalateFromLevel

	tiers := make([]*model.OncallEscalationTierM, 0, len(rq.Tiers))
	for i, tier := range rq.Tiers {
		tiers = append(tiers, &model.OncallEscalationTierM{
			GroupName:       rq.Group,
			TierIndex:       int64(i),
			TimeoutSeconds:  tier.TimeoutSeconds,
			TargetType:      tier.TargetType,
			TargetGroupName: tier.GroupName,
			UserNames:       model.EncodeNameList(tier.UserNames),
		})
	}
	if err := b.st.OncallSchedules().SaveEscalation(ctx, &schedule, tiers); err != nil {
		return nil, storeerr.Write(err)
	}
	return b.GetEscalation(ctx, &api.GetOncallEscalationRequest{Group: rq.Group})
}

func (b *dutyScheduleBiz) Escalate(
	ctx context.Context, rq *api.EscalateRequest,
) (*api.EscalateResponse, error) {
	if err := b.requireGroup(ctx, rq.Group); err != nil {
		return nil, err
	}
	if b.escalator == nil {
		return nil, errno.ErrInvalidArgument.WithMessage("escalation is not enabled in this deployment")
	}
	return b.escalator.EscalateNow(ctx, rq.Group, rq.TicketID)
}

func (b *dutyScheduleBiz) group(ctx context.Context, name string) (*model.OncallGroupM, error) {
	group, err := b.st.OncallGroups().GetByName(ctx, name)
	if err := storeerr.GetError(group, err, "on-call group %q not found", name); err != nil {
		return nil, err
	}
	return group, nil
}

func (b *dutyScheduleBiz) requireGroup(ctx context.Context, name string) error {
	_, err := b.group(ctx, name)
	return err
}

func (b *dutyScheduleBiz) nativeAggregate(
	ctx context.Context, name string,
) (*model.OncallScheduleAggregate, error) {
	if err := b.requireGroup(ctx, name); err != nil {
		return nil, err
	}
	agg, err := b.st.OncallSchedules().GetAggregate(ctx, name)
	if err != nil {
		return nil, storeerr.Read(err)
	}
	if agg == nil || agg.Schedule == nil {
		return nil, errno.ErrInvalidArgument.WithMessage("on-call group %q has no native schedule", name)
	}
	return agg, nil
}

func (b *dutyScheduleBiz) response(
	groupName string, group *model.OncallGroupM, agg *model.OncallScheduleAggregate,
) *api.OncallScheduleResponse {
	resp := &api.OncallScheduleResponse{
		Group:            groupName,
		Source:           sourceOf(group, agg),
		LegacyScheduleID: group.ScheduleID,
	}
	if agg != nil && agg.Schedule != nil {
		resp.Configured = true
		resp.Schedule = scheduleOf(agg)
	}
	return resp
}

func (b *dutyScheduleBiz) current(
	groupName string, group *model.OncallGroupM, agg *model.OncallScheduleAggregate, at time.Time,
) *api.OncallCurrentResponse {
	at = at.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	resp := &api.OncallCurrentResponse{Group: groupName, At: at, Source: sourceOf(group, agg)}
	if agg != nil && agg.Schedule != nil {
		resp.Timezone = agg.Schedule.Timezone
	}

	if agg != nil && agg.Schedule != nil {
		watchers := oncall.WatchersAt(agg, at)
		if len(watchers) == 0 {
			return resp
		}
		resp.Current = memberOf(watchers[0])
		for _, watcher := range watchers[1:] {
			resp.Backups = append(resp.Backups, *memberOf(watcher))
		}
		return resp
	}

	// A legacy group keeps answering from the in-memory duty store until it is
	// imported. There is no durable since/until for a provider roster.
	if b.legacy == nil {
		return resp
	}
	names, ok := b.legacy.LegacyWatchers(groupName)
	if !ok || len(names) == 0 {
		return resp
	}
	resp.Current = &api.OncallCurrentMember{UserName: names[0]}
	for _, name := range names[1:] {
		resp.Backups = append(resp.Backups, api.OncallCurrentMember{UserName: name})
	}
	return resp
}

func sourceOf(group *model.OncallGroupM, agg *model.OncallScheduleAggregate) string {
	if agg != nil && agg.Schedule != nil {
		return "native"
	}
	if group != nil && strings.TrimSpace(group.ScheduleID) != "" {
		return "regen"
	}
	return "none"
}

func (b *dutyScheduleBiz) refreshRoster(ctx context.Context) {
	if b.refresher == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	runtime.GoSafe(ctx, "chat.oncall.refresh", func() {
		if err := b.refresher.Refresh(ctx); err != nil {
			slog.WarnContext(ctx, "on-call: refresh the duty roster after a schedule change", "error", err)
		}
	})
}

func definitionOf(
	group string, def *api.OncallScheduleDefinition,
) *model.OncallScheduleDefinition {
	now := time.Now().UnixMilli()
	out := &model.OncallScheduleDefinition{
		Schedule: &model.OncallScheduleM{
			GroupName:           group,
			Timezone:            def.Timezone,
			Description:         def.Description,
			NotificationChannel: def.NotificationChannel,
			CreatedAt:           now,
			UpdatedAt:           now,
		},
		Layers:       make([]*model.OncallScheduleLayerM, 0, len(def.Layers)),
		Participants: make(map[int][]*model.OncallScheduleParticipantM, len(def.Layers)),
	}
	for i, layer := range def.Layers {
		out.Layers = append(out.Layers, &model.OncallScheduleLayerM{
			GroupName:            group,
			Name:                 layer.Name,
			OrderIndex:           int64(i),
			RotationType:         layer.RotationType,
			RotationStart:        layer.RotationStart.UTC(),
			ShiftDurationSeconds: layer.ShiftDurationSeconds,
			CreatedAt:            now,
		})
		participants := make([]*model.OncallScheduleParticipantM, 0, len(layer.Participants))
		for j, participant := range layer.Participants {
			participants = append(participants, &model.OncallScheduleParticipantM{
				UserName:   participant.UserName,
				OrderIndex: int64(j),
				CreatedAt:  now,
			})
		}
		out.Participants[i] = participants
	}
	return out
}

func aggregateOfDefinition(def *model.OncallScheduleDefinition) *model.OncallScheduleAggregate {
	layers := make([]model.OncallScheduleLayerAggregate, 0, len(def.Layers))
	for i, layer := range def.Layers {
		layers = append(layers, model.OncallScheduleLayerAggregate{
			Layer:        *layer,
			Participants: def.Participants[i],
		})
	}
	return &model.OncallScheduleAggregate{Schedule: def.Schedule, Layers: layers}
}

func scheduleOf(agg *model.OncallScheduleAggregate) *api.OncallSchedule {
	out := &api.OncallSchedule{
		OncallScheduleDefinition: api.OncallScheduleDefinition{
			Timezone:            agg.Schedule.Timezone,
			Description:         agg.Schedule.Description,
			NotificationChannel: agg.Schedule.NotificationChannel,
			Layers:              make([]api.OncallScheduleLayer, 0, len(agg.Layers)),
		},
		Overrides:        make([]api.OncallScheduleOverride, 0, len(agg.Overrides)),
		Unavailabilities: make([]api.OncallScheduleUnavailability, 0, len(agg.Unavailabilities)),
		Holidays:         make([]api.OncallScheduleHoliday, 0, len(agg.Holidays)),
	}
	for _, layer := range agg.Layers {
		wire := api.OncallScheduleLayer{
			Name:                 layer.Layer.Name,
			OrderIndex:           layer.Layer.OrderIndex,
			RotationType:         layer.Layer.RotationType,
			RotationStart:        layer.Layer.RotationStart.UTC(),
			ShiftDurationSeconds: layer.Layer.ShiftDurationSeconds,
			Participants:         make([]api.OncallScheduleParticipant, 0, len(layer.Participants)),
		}
		for _, participant := range layer.Participants {
			wire.Participants = append(wire.Participants, api.OncallScheduleParticipant{
				UserName:   participant.UserName,
				OrderIndex: participant.OrderIndex,
			})
		}
		out.Layers = append(out.Layers, wire)
	}
	for _, override := range agg.Overrides {
		out.Overrides = append(out.Overrides, overrideOf(override))
	}
	for _, unavailability := range agg.Unavailabilities {
		out.Unavailabilities = append(out.Unavailabilities, unavailabilityOf(unavailability))
	}
	for _, holiday := range agg.Holidays {
		out.Holidays = append(out.Holidays, holidayOf(holiday))
	}
	return out
}

func overrideOf(row *model.OncallScheduleOverrideM) api.OncallScheduleOverride {
	out := api.OncallScheduleOverride{
		ID:           row.ID,
		OverrideUser: row.OverrideUser,
		StartTime:    row.StartTime.UTC(),
		EndTime:      row.EndTime.UTC(),
		CreatedBy:    row.CreatedBy,
	}
	if row.CreatedAt > 0 {
		out.CreatedAt = time.UnixMilli(row.CreatedAt).UTC()
	}
	return out
}

func unavailabilityOf(row *model.OncallScheduleUnavailabilityM) api.OncallScheduleUnavailability {
	return api.OncallScheduleUnavailability{
		ID:        row.ID,
		UserName:  row.UserName,
		StartDate: row.StartDate,
		EndDate:   row.EndDate,
		Reason:    row.Reason,
		CreatedBy: row.CreatedBy,
	}
}

func holidayOf(row *model.OncallScheduleHolidayM) api.OncallScheduleHoliday {
	return api.OncallScheduleHoliday{
		ID:          row.ID,
		Date:        row.Date,
		Name:        row.Name,
		CountryCode: row.CountryCode,
	}
}

func policyOf(agg *model.OncallScheduleAggregate) *api.OncallEscalationPolicy {
	out := &api.OncallEscalationPolicy{
		Enabled:               agg.Schedule.EscalationEnabled,
		AutoEscalate:          agg.Schedule.EscalationAuto,
		AutoEscalateFromLevel: agg.Schedule.EscalationFromLevel,
		Tiers:                 make([]api.OncallEscalationTier, 0, len(agg.Tiers)),
	}
	for _, tier := range agg.Tiers {
		out.Tiers = append(out.Tiers, api.OncallEscalationTier{
			Index:          tier.TierIndex,
			TimeoutSeconds: tier.TimeoutSeconds,
			TargetType:     tier.TargetType,
			GroupName:      tier.TargetGroupName,
			UserNames:      tier.UserList(),
		})
	}
	return out
}

func memberOf(current oncall.Current) *api.OncallCurrentMember {
	out := &api.OncallCurrentMember{
		UserName:   current.UserName,
		IsOverride: current.IsOverride,
		LayerName:  current.LayerName,
	}
	if !current.Since.IsZero() {
		since := current.Since.UTC()
		out.Since = &since
	}
	if current.Until != nil {
		until := current.Until.UTC()
		out.Until = &until
	}
	return out
}

func segmentsOf(segments []oncall.Segment) []api.OncallSegment {
	out := make([]api.OncallSegment, 0, len(segments))
	for _, segment := range segments {
		out = append(out, api.OncallSegment{
			Start:      segment.Start.UTC(),
			End:        segment.End.UTC(),
			UserName:   segment.UserName,
			IsOverride: segment.IsOverride,
			LayerName:  segment.LayerName,
		})
	}
	return out
}
