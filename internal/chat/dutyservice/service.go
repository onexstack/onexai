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

// Package dutyservice implements the command-facing duty capability.
//
// The native on-call schedule is the primary implementation: it reads the same
// SQL tables the console page manages, so a command sees a schedule edit
// immediately rather than after the legacy five-minute refresh. The in-memory
// duty store is kept behind it for groups whose schedule_id still points at a
// legacy Regen schedule.
package dutyservice

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/biz/v1/dutyschedule"
	"github.com/onexstack/onexai/internal/chat/duty"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/userdir"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// isNotFound reports whether err is the store's "no such row" answer. The
// generic store and the biz layer spell it differently, so both are accepted.
func isNotFound(err error) bool {
	return errors.Is(err, errno.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
}

// groupStore is the slice of the on-call group store the service needs.
type groupStore interface {
	ListAll(ctx context.Context) ([]*model.OncallGroupM, error)
	GetByName(ctx context.Context, name string) (*model.OncallGroupM, error)
}

// scheduleStore is the slice of the native schedule store the service needs.
type scheduleStore interface {
	ListAggregates(ctx context.Context) ([]*model.OncallScheduleAggregate, error)
	GetAggregate(ctx context.Context, group string) (*model.OncallScheduleAggregate, error)
}

// service implements duty.Service.
type service struct {
	groups    groupStore
	schedules scheduleStore
	legacy    *duty.Store
	biz       dutyschedule.DutyScheduleBiz
	resolve   userdir.Resolver

	// legacyOnly forces every read through the in-memory store. It is what a
	// deployment that has not enabled native schedules gets, and what the
	// command tests use to keep exercising the legacy paths.
	legacyOnly bool
}

var _ duty.Service = (*service)(nil)

// New returns a service backed by the native schedule tables with the legacy
// store as fallback. Any argument except legacy may be nil: a nil groups or
// schedules store degrades to legacy-only behaviour.
func New(
	groups groupStore, schedules scheduleStore, legacy *duty.Store,
	biz dutyschedule.DutyScheduleBiz, resolve userdir.Resolver,
) duty.Service {
	return &service{
		groups:    groups,
		schedules: schedules,
		legacy:    legacy,
		biz:       biz,
		resolve:   resolve,
	}
}

// NewLegacyOnly returns a service that answers exclusively from the in-memory
// duty store. It is the rollback / test shape; schedule methods are unavailable.
func NewLegacyOnly(legacy *duty.Store) duty.Service {
	return &service{legacy: legacy, legacyOnly: true}
}

// nativeEnabled reports whether the native read path can be used at all.
func (s *service) nativeEnabled() bool {
	return !s.legacyOnly && s.groups != nil && s.schedules != nil
}

// legacyGet returns the legacy view of a group, if the store knows it.
func (s *service) legacyGet(name string) (duty.Group, bool) {
	if s.legacy == nil {
		return duty.Group{}, false
	}
	return s.legacy.GetGroupInfo(name)
}

// lookupRow resolves a group row case-insensitively. A not-found answer is
// (nil, nil); any other storage error is returned so the caller can decide
// whether a legacy fallback covers it.
func (s *service) lookupRow(ctx context.Context, name string) (*model.OncallGroupM, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	row, err := s.groups.GetByName(ctx, name)
	if err == nil && row != nil {
		return row, nil
	}
	if err != nil && !isNotFound(err) {
		return nil, err
	}
	lower := strings.ToLower(name)
	if lower != name {
		row, err = s.groups.GetByName(ctx, lower)
		if err == nil && row != nil {
			return row, nil
		}
		if err != nil && !isNotFound(err) {
			return nil, err
		}
	}
	return nil, nil
}

// HasKnownGroups implements duty.RosterReader.
func (s *service) HasKnownGroups(ctx context.Context) (bool, error) {
	if !s.nativeEnabled() {
		return s.legacy != nil && s.legacy.HasKnownGroups(), nil
	}
	rows, err := s.groups.ListAll(ctx)
	if err != nil {
		if s.legacy != nil && s.legacy.HasKnownGroups() {
			slog.WarnContext(ctx, "duty: group table unavailable, serving the legacy roster", "error", err)
			return true, nil
		}
		return false, err
	}
	if len(rows) > 0 {
		return true, nil
	}
	return s.legacy != nil && s.legacy.HasKnownGroups(), nil
}

// GetGroupInfo implements duty.RosterReader.
func (s *service) GetGroupInfo(ctx context.Context, groupID string) (duty.Group, bool, error) {
	if !s.nativeEnabled() {
		g, ok := s.legacyGet(groupID)
		return g, ok, nil
	}

	row, err := s.lookupRow(ctx, groupID)
	if err != nil {
		if g, ok := s.legacyGet(groupID); ok {
			slog.WarnContext(ctx, "duty: group lookup failed, serving the legacy roster",
				"group", groupID, "error", err)
			return g, true, nil
		}
		return duty.Group{}, false, err
	}
	if row == nil {
		g, ok := s.legacyGet(groupID)
		return g, ok, nil
	}

	g := duty.BuildGroup(row)
	agg, err := s.schedules.GetAggregate(ctx, row.Name)
	if err != nil {
		slog.WarnContext(ctx, "duty: native schedule unavailable, serving the legacy roster",
			"group", groupID, "error", err)
		s.applyLegacyWatchers(&g, groupID, true)
		return g, true, nil
	}
	if agg != nil && agg.Schedule != nil {
		duty.ApplyNativeRoster(ctx, &g, agg, time.Now().UTC(), s.resolve)
		return g, true, nil
	}
	// A nil aggregate without an error is a group with no native schedule. Do
	// not replay a legacy "native" snapshot: the schedule may have just been
	// deleted, and the live table is authoritative.
	s.applyLegacyWatchers(&g, groupID, false)
	return g, true, nil
}

// GetGroupMeta implements duty.RosterReader. It intentionally does not touch
// the schedule tables: the message path calls it on every inbound message.
func (s *service) GetGroupMeta(ctx context.Context, groupID string) (duty.Group, bool, error) {
	if !s.nativeEnabled() {
		g, ok := s.legacyGet(groupID)
		return g, ok, nil
	}
	row, err := s.lookupRow(ctx, groupID)
	if err != nil {
		if g, ok := s.legacyGet(groupID); ok {
			return g, true, nil
		}
		return duty.Group{}, false, err
	}
	if row == nil {
		g, ok := s.legacyGet(groupID)
		return g, ok, nil
	}
	g := duty.BuildGroup(row)
	if lg, ok := s.legacyGet(groupID); ok {
		g.ParentGroupID = lg.ParentGroupID
	}
	return g, true, nil
}

// ListGroupInfos implements duty.RosterReader.
func (s *service) ListGroupInfos(ctx context.Context) ([]duty.Group, error) {
	if !s.nativeEnabled() {
		return s.legacyGroupInfos(), nil
	}

	rows, err := s.groups.ListAll(ctx)
	if err != nil {
		if s.legacy != nil && s.legacy.HasKnownGroups() {
			slog.WarnContext(ctx, "duty: group table unavailable, serving the legacy roster", "error", err)
			return s.legacyGroupInfos(), nil
		}
		return nil, err
	}
	if len(rows) == 0 {
		return s.legacyGroupInfos(), nil
	}

	aggs, err := s.schedules.ListAggregates(ctx)
	scheduleErr := err != nil
	if err != nil {
		slog.WarnContext(ctx, "duty: native schedules unavailable, serving legacy rosters where possible", "error", err)
		aggs = nil
	}
	byGroup := make(map[string]*model.OncallScheduleAggregate, len(aggs))
	for _, agg := range aggs {
		if agg != nil && agg.Schedule != nil {
			byGroup[agg.Schedule.GroupName] = agg
		}
	}

	now := time.Now().UTC()
	out := make([]duty.Group, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		g := duty.BuildGroup(row)
		if agg := byGroup[row.Name]; agg != nil {
			duty.ApplyNativeRoster(ctx, &g, agg, now, s.resolve)
			out = append(out, g)
			continue
		}
		s.applyLegacyWatchers(&g, row.Name, scheduleErr)
		out = append(out, g)
	}
	sortGroups(out)
	return out, nil
}

// ActiveGroupInfos implements duty.RosterReader.
func (s *service) ActiveGroupInfos(ctx context.Context) ([]duty.Group, error) {
	groups, err := s.ListGroupInfos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]duty.Group, 0, len(groups))
	for _, g := range groups {
		if len(g.Watchers) > 0 {
			out = append(out, g)
		}
	}
	return out, nil
}

// FilterGroups implements duty.RosterReader.
func (s *service) FilterGroups(ctx context.Context, filter string) ([]duty.Group, bool, error) {
	groups, err := s.ActiveGroupInfos(ctx)
	if err != nil {
		return nil, false, err
	}
	re, ok := compileFilter(filter)
	if !ok {
		return nil, false, nil
	}
	if re == nil {
		return groups, true, nil
	}
	out := make([]duty.Group, 0, len(groups))
	for _, g := range groups {
		if re.MatchString(g.ID) || re.MatchString(g.Name) ||
			re.MatchString(g.ShortDesc) || re.MatchString(g.LongDesc) {
			out = append(out, g)
		}
	}
	return out, true, nil
}

// ActiveGroupIDs implements duty.RosterReader.
func (s *service) ActiveGroupIDs(ctx context.Context) (map[string]struct{}, error) {
	groups, err := s.ActiveGroupInfos(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		out[strings.ToLower(g.ID)] = struct{}{}
	}
	return out, nil
}

// RosterError implements duty.RosterReader. Only the legacy provider can report
// a roster read failure; native errors are returned by the calls that hit them.
func (s *service) RosterError() error {
	if s.legacy == nil {
		return nil
	}
	return s.legacy.RosterError()
}

// applyLegacyWatchers copies the legacy roster onto a group that has no native
// schedule. A group the legacy store does not know keeps an empty roster.
//
// A legacy snapshot whose own source is "native" is ignored when
// allowNativeSnapshot is false: the live table is authoritative, so a native
// schedule that was just deleted must answer "no rotation" rather than replay
// the last five-minute snapshot. It is allowed back when the schedule query
// itself failed, where the snapshot is stale but better than claiming nobody is
// on call.
func (s *service) applyLegacyWatchers(g *duty.Group, name string, allowNativeSnapshot bool) {
	lg, ok := s.legacyGet(name)
	if !ok {
		return
	}
	if !allowNativeSnapshot && lg.ScheduleSource == "native" {
		return
	}
	g.Watchers = lg.Watchers
	g.ParentGroupID = lg.ParentGroupID
	if g.ScheduleID == "" {
		g.ScheduleID = lg.ScheduleID
	}
	if lg.ScheduleSource != "" {
		g.ScheduleSource = lg.ScheduleSource
	}
	g.PrimaryUntil = lg.PrimaryUntil
	g.PrimaryIsOverride = lg.PrimaryIsOverride
	g.PrimaryLayerName = lg.PrimaryLayerName
}

func (s *service) legacyGroupInfos() []duty.Group {
	if s.legacy == nil {
		return nil
	}
	return s.legacy.GroupInfos()
}

func sortGroups(groups []duty.Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Order != groups[j].Order {
			return groups[i].Order < groups[j].Order
		}
		return groups[i].ID < groups[j].ID
	})
}
