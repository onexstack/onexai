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

package duty

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/userdir"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	duty "github.com/onexstack/onexai/pkg/duty"
)

// moduleLister is the slice of the on-call group store the duty source needs.
// Keeping it narrow lets tests pass a fake without standing up the whole store.
type moduleLister interface {
	ListAll(ctx context.Context) ([]*model.OncallGroupM, error)
}

// scheduleLister is the slice of the native schedule store the duty source
// needs. It is optional: a deployment that has not enabled native schedules
// keeps the Regen-only behaviour.
type scheduleLister interface {
	ListAggregates(ctx context.Context) ([]*model.OncallScheduleAggregate, error)
}

// moduleSource builds duty groups from the oncall-group table.
//
// A group with a native schedule is evaluated locally and never touches the
// provider. A group whose schedule_id still points at Regen falls back to the
// provider, so the two can coexist during migration.
type moduleSource struct {
	mods      moduleLister
	schedules scheduleLister
	provider  duty.Roster
	resolve   userdir.Resolver

	// rosterHealth carries the verdict of the last Load for the store to read.
	rosterHealth

	// includeOrphanSchedules, when set, also emits provider schedules that no
	// module points at. Off by default: an unlinked schedule is usually a
	// leftover, and the module table is the source of truth for what /duty and
	// /oncall --list should show.
	includeOrphanSchedules bool
}

// NewModuleSource returns a Source backed by on-call group rows and a duty
// provider, without native schedules. It is kept for callers and tests that
// only exercise the legacy provider path.
func NewModuleSource(mods moduleLister, roster duty.Roster, resolve userdir.Resolver) Source {
	return NewModuleSourceWithSchedules(mods, nil, roster, resolve)
}

// NewModuleSourceWithSchedules returns a Source backed by on-call group rows,
// native schedules and a duty provider.
//
// When mods is nil — a deployment with no group store — it falls back to the
// provider-only source so the duty commands keep working off schedule names alone.
func NewModuleSourceWithSchedules(
	mods moduleLister, schedules scheduleLister, roster duty.Roster, resolve userdir.Resolver,
) Source {
	if mods == nil {
		return NewProviderSource(roster, resolve)
	}
	return &moduleSource{mods: mods, schedules: schedules, provider: roster, resolve: resolve}
}

// Load reads every group row, its native schedule (when configured) and the
// roster of the legacy provider schedule it points at (when it has one).
//
// A group whose roster lookup fails is still returned, just without watchers:
// one broken rotation must not blank out the other groups.
func (s *moduleSource) Load(ctx context.Context) ([]Group, error) {
	mods, err := s.mods.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("load oncall groups: %w", err)
	}

	// Cleared up front so a pass that reads every roster stops reporting the
	// previous failure.
	s.setRosterError(nil)

	native := s.loadNative(ctx)
	now := time.Now().UTC()

	var providerSchedules []duty.Schedule
	if s.providerConfigured() && needsProvider(mods, native) {
		providerSchedules, err = s.provider.Schedules(ctx)
		if err != nil {
			// The provider being down must not hide the module metadata or the
			// native schedules; the loop fills the legacy rosters in on the next
			// tick.
			slog.WarnContext(ctx, "duty: schedules unavailable, listing modules without legacy watchers",
				"error", err, "modules", len(mods))
			s.setRosterError(err)
			providerSchedules = nil
		}
	}
	byID, byName := indexSchedules(providerSchedules)

	groups := make([]Group, 0, len(mods))
	seen := make(map[string]struct{}, len(mods))
	for _, m := range mods {
		if m == nil {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(m.Name))
		if id == "" {
			continue
		}
		seen[id] = struct{}{}

		name := strings.TrimSpace(m.DisplayName)
		if name == "" {
			name = m.Name
		}

		g := Group{
			ID:                 id,
			Name:               name,
			RawName:            m.Name,
			Namespace:          m.Namespace,
			ScheduleID:         strings.TrimSpace(m.ScheduleID),
			ShortDesc:          m.ShortDesc,
			LongDesc:           m.LongDesc,
			WelcomeMessage:     m.WelcomeMessage,
			Agents:             m.AgentList(),
			Owner:              m.Owner,
			Order:              m.Order,
			AllocationStrategy: m.AllocationStrategy,
		}

		if agg := native[m.Name]; agg != nil && agg.Schedule != nil {
			g.ScheduleSource = "native"
			ApplyNativeRoster(ctx, &g, agg, now, s.resolve)
			groups = append(groups, g)
			continue
		}

		g.ScheduleSource = "none"
		if g.ScheduleID != "" {
			g.ScheduleSource = "regen"
		}
		if s.providerConfigured() {
			g.ScheduleID = s.resolveScheduleID(ctx, byID, byName, id, g.ScheduleID)
			g.Watchers = s.roster(ctx, byID, g.ScheduleID)
			if g.ScheduleID == "" {
				g.ScheduleSource = "none"
			}
		}
		groups = append(groups, g)
	}

	// An empty group table is the state a deployment sits in before its group
	// rows exist. Falling back to the provider schedules keeps the duty commands
	// answering meanwhile.
	emitAll := s.includeOrphanSchedules
	if len(mods) == 0 && len(providerSchedules) > 0 {
		slog.WarnContext(ctx, "duty: no oncall groups configured, listing schedules instead", "count", len(providerSchedules))
		emitAll = true
	} else if len(mods) == 0 && len(native) == 0 && len(providerSchedules) == 0 {
		slog.WarnContext(ctx, "duty: oncall group table is empty and no schedules were loaded")
	}

	if emitAll {
		for _, sc := range providerSchedules {
			id := strings.ToLower(strings.TrimSpace(sc.Name))
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			groups = append(groups, Group{
				ID:             id,
				Name:           sc.Name,
				ScheduleID:     sc.ID,
				ScheduleSource: "regen",
				Watchers:       s.roster(ctx, byID, sc.ID),
			})
		}
	}
	return groups, nil
}

// providerConfigured reports whether the provider is usable. The Roster
// interface is deliberately narrower than Provider, so a test fake that only
// implements Roster is treated as configured.
func (s *moduleSource) providerConfigured() bool {
	if s.provider == nil {
		return false
	}
	if p, ok := s.provider.(interface{ Configured() bool }); ok {
		return p.Configured()
	}
	return true
}

func (s *moduleSource) loadNative(ctx context.Context) map[string]*model.OncallScheduleAggregate {
	out := make(map[string]*model.OncallScheduleAggregate)
	if s.schedules == nil {
		return out
	}
	aggs, err := s.schedules.ListAggregates(ctx)
	if err != nil {
		// Native schedules failing is a storage problem, not a reason to hide
		// the group metadata; legacy groups still resolve.
		slog.WarnContext(ctx, "duty: native schedules unavailable, using legacy rosters only", "error", err)
		return out
	}
	for _, agg := range aggs {
		if agg != nil && agg.Schedule != nil {
			out[agg.Schedule.GroupName] = agg
		}
	}
	return out
}

// needsProvider reports whether any group still depends on the provider.
func needsProvider(mods []*model.OncallGroupM, native map[string]*model.OncallScheduleAggregate) bool {
	if len(mods) == 0 {
		return true
	}
	for _, m := range mods {
		if m == nil {
			continue
		}
		if agg := native[m.Name]; agg == nil || agg.Schedule == nil {
			return true
		}
	}
	return false
}

func indexSchedules(schedules []duty.Schedule) (byID, byName map[string]duty.Schedule) {
	byID = make(map[string]duty.Schedule, len(schedules))
	byName = make(map[string]duty.Schedule, len(schedules))
	for _, sc := range schedules {
		byID[sc.ID] = sc
		name := strings.ToLower(strings.TrimSpace(sc.Name))
		if name == "" {
			continue
		}
		if kept, ok := byName[name]; ok {
			slog.Warn("duty: multiple schedules share a name, keeping the first",
				"name", sc.Name, "kept", kept.ID, "skipped", sc.ID)
			continue
		}
		byName[name] = sc
	}
	return byID, byName
}

// resolveScheduleID returns the provider schedule id whose roster backs a module.
//
// An explicit schedule_id is authoritative: when it is set it is used, and when
// it is set but unknown the module is left without a roster rather than being
// attached to a same-named schedule, which could page the wrong person. Only an
// empty schedule_id falls back to the schedule whose name matches the module
// id.
func (s *moduleSource) resolveScheduleID(ctx context.Context, byID, byName map[string]duty.Schedule, id, explicit string) string {
	if explicit != "" {
		if _, ok := byID[explicit]; ok {
			return explicit
		}
		slog.WarnContext(ctx, "duty: module points at a schedule that does not exist",
			"module", id, "schedule_id", explicit)
		return ""
	}
	sc, ok := byName[id]
	if !ok {
		// A module with no rotation of its own: legitimate, and warning on it
		// would fire on every refresh for every such module.
		return ""
	}
	slog.InfoContext(ctx, "duty: resolving module roster by schedule name; schedule_id is empty",
		"module", id, "schedule", sc.ID)
	return sc.ID
}

// roster resolves the watcher list for a provider schedule id, tolerating an empty
// id, an unknown schedule and a lookup failure — each yields no watcher with a
// warning rather than an error, so one broken rotation never blanks the rest.
func (s *moduleSource) roster(ctx context.Context, byID map[string]duty.Schedule, scheduleID string) []*v1.Watcher {
	if scheduleID == "" {
		return nil
	}
	if _, ok := byID[scheduleID]; !ok {
		slog.WarnContext(ctx, "duty: module points at a schedule that does not exist",
			"schedule_id", scheduleID)
		return nil
	}
	oncallPerson, err := s.provider.OnCall(ctx, scheduleID)
	if err != nil {
		slog.WarnContext(ctx, "duty: failed to read the on-call user for a schedule",
			"schedule_id", scheduleID, "error", err)
		// A schedule that does not exist is a data problem and is reported as
		// one above, without marking the roster backend unhealthy; a call that
		// failed is the backend, and no group's roster can be vouched for.
		s.setRosterError(err)
		return nil
	}
	if name := strings.TrimSpace(oncallPerson.Name); name != "" {
		return []*v1.Watcher{NewWatcher(ctx, name, s.resolve)}
	}
	return nil
}
