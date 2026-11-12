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
	"errors"
	"time"

	"github.com/onexstack/onexai/internal/chat/api"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// ErrScheduleUnavailable reports that the deployment wired no native schedule
// service. It is only reachable in tests and in a deliberately degraded
// deployment; the real wiring always provides one.
var ErrScheduleUnavailable = errors.New("duty schedule service is not configured")

// RosterReader is the read side of the duty capability used by the chat
// commands. It answers "which groups exist and who is on call right now".
//
// The implementation is expected to read the authoritative source on each call:
// the native schedule tables when a group has a native rotation, and the legacy
// in-memory store only as a fallback for groups that still point at Regen.
type RosterReader interface {
	// HasKnownGroups reports whether any group metadata is available at all.
	// It is how a command tells "the service is unavailable" apart from "the
	// name you typed does not exist". An error means the authoritative source
	// could not be read and no fallback covered it.
	HasKnownGroups(ctx context.Context) (bool, error)

	// GetGroupInfo returns the metadata and current watchers for a group id
	// (case-insensitive). The bool is false when no such group is known.
	GetGroupInfo(ctx context.Context, groupID string) (Group, bool, error)

	// GetGroupMeta returns metadata only, without evaluating the rotation. It
	// is the hot-path lookup: a caller that only needs a group's agents or
	// descriptions must not pay for the schedule sub-table queries.
	GetGroupMeta(ctx context.Context, groupID string) (Group, bool, error)

	// ListGroupInfos returns every known group with its current watchers.
	ListGroupInfos(ctx context.Context) ([]Group, error)

	// ActiveGroupInfos returns the groups that currently have at least one
	// watcher, sorted by (Order, ID).
	ActiveGroupInfos(ctx context.Context) ([]Group, error)

	// FilterGroups returns the active groups matching a case-insensitive
	// regular expression. An empty filter returns every active group. The bool
	// reports whether the pattern compiled.
	FilterGroups(ctx context.Context, filter string) ([]Group, bool, error)

	// ActiveGroupIDs returns the set of lower-cased group ids with watchers.
	ActiveGroupIDs(ctx context.Context) (map[string]struct{}, error)

	// RosterError reports why the legacy roster could not be read, or nil. It
	// never reports native schedule errors: those are returned by the calls
	// that hit them.
	RosterError() error
}

// ScheduleService exposes the native on-call schedule capability — the same
// capability the console page drives through REST — to the chat commands.
type ScheduleService interface {
	GetSchedule(ctx context.Context, group string) (*api.OncallScheduleResponse, error)
	SaveSchedule(ctx context.Context, group string, def *api.OncallScheduleDefinition) (*api.OncallScheduleResponse, error)
	PreviewSchedule(
		ctx context.Context, group string, def *api.OncallScheduleDefinition, from, to time.Time,
	) (*api.OncallTimelineResponse, error)

	GetCurrent(ctx context.Context, group string, at time.Time) (*api.OncallCurrentResponse, error)
	ListCurrent(ctx context.Context, names []string, at time.Time) (*api.ListOncallCurrentResponse, error)
	GetTimeline(ctx context.Context, group string, from, to time.Time) (*api.OncallTimelineResponse, error)
	GetLayerTimelines(
		ctx context.Context, group string, from, to time.Time,
	) (*api.OncallLayerTimelinesResponse, error)

	ListOverrides(ctx context.Context, group string) (*api.OverrideListResponse, error)
	CreateOverride(
		ctx context.Context, group, user string, start, end time.Time, createdBy string,
	) (*api.OncallScheduleOverride, error)
	DeleteOverride(ctx context.Context, group, id string) error

	ListUnavailabilities(ctx context.Context, group string) (*api.UnavailabilityListResponse, error)
	CreateUnavailability(
		ctx context.Context, group, user, startDate, endDate, reason, createdBy string,
	) (*api.OncallScheduleUnavailability, error)
	DeleteUnavailability(ctx context.Context, group, id string) error

	ListHolidays(ctx context.Context, group string) (*api.HolidayListResponse, error)
	CreateHoliday(ctx context.Context, group, date, name, country string) (*api.OncallScheduleHoliday, error)
	DeleteHoliday(ctx context.Context, group, id string) error

	GetEscalation(ctx context.Context, group string) (*api.OncallEscalationResponse, error)
	SaveEscalation(
		ctx context.Context, group string, policy *api.OncallEscalationPolicy,
	) (*api.OncallEscalationResponse, error)
	Escalate(ctx context.Context, group, ticketID string) (*api.EscalateResponse, error)
}

// Service is what the command layer consumes. One implementation (the native
// schedule service) is the console page's capability; the in-memory store is
// kept only as the Regen fallback behind it.
type Service interface {
	RosterReader
	ScheduleService
}

// Primary returns the first watcher, or the placeholder the ticket-creation
// path has always used when a group has no rotation right now.
func (g Group) Primary() *v1.Watcher {
	for _, w := range g.Watchers {
		if w != nil {
			return w
		}
	}
	return &v1.Watcher{UserId: "default_user", UserName: "Default User"}
}

// AssignStrategy maps the group's configured strategy to the proto enum.
func (g Group) AssignStrategy() v1.AssignStrategy {
	return AssignStrategyOf(g.AllocationStrategy)
}

// OnDutyGroup returns the flat group shape older call sites use.
func (g Group) OnDutyGroup() *v1.OnDutyGroup {
	return &v1.OnDutyGroup{
		GroupId:        g.ID,
		GroupName:      g.Name,
		ShortDesc:      g.ShortDesc,
		LongDesc:       g.LongDesc,
		ParentGroupId:  g.ParentGroupID,
		AssignStrategy: g.AssignStrategy(),
	}
}
