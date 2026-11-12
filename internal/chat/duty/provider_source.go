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

	"github.com/onexstack/onexai/internal/chat/userdir"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
	duty "github.com/onexstack/onexai/pkg/duty"
)

// providerSource loads duty groups from a duty provider: a schedule's name is
// the module id and the person currently on call for it is the group's watcher.
type providerSource struct {
	roster  duty.Roster
	resolve userdir.Resolver

	// rosterHealth carries the verdict of the last Load for the store to read.
	rosterHealth
}

// NewProviderSource returns a Source backed by a duty provider.
//
// resolve translates an on-call name (an email or a login name) into the Lark
// user_id a ticket group is created with. Providers store participants as free
// text, so without a resolution the raw name is used and Lark is likely to
// reject it.
func NewProviderSource(roster duty.Roster, resolve userdir.Resolver) Source {
	return &providerSource{roster: roster, resolve: resolve}
}

// Load reads every schedule and its current on-call user.
//
// A schedule whose on-call lookup fails is still reported, just without
// watchers: one broken rotation must not blank out the other modules.
func (s *providerSource) Load(ctx context.Context) ([]Group, error) {
	schedules, err := s.roster.Schedules(ctx)
	if err != nil {
		return nil, fmt.Errorf("load duty schedules: %w", err)
	}

	// Cleared up front so a pass that reads every roster stops reporting the
	// previous failure.
	s.setRosterError(nil)

	groups := make([]Group, 0, len(schedules))
	for _, schedule := range schedules {
		// The command layer passes a lower-cased module id, and the store
		// keys watchers by the exact id while HasGroup compares
		// case-insensitively; normalising here keeps the two in step.
		id := strings.ToLower(strings.TrimSpace(schedule.Name))
		if id == "" {
			continue
		}
		group := Group{ID: id, Name: schedule.Name}

		oncall, err := s.roster.OnCall(ctx, schedule.ID)
		if err != nil {
			slog.WarnContext(ctx, "duty: failed to read the on-call user for a schedule",
				"schedule", schedule.Name, "error", err)
			// A failed call is the backend, not the rotation: recorded so the
			// store knows the empty watcher list below says nothing about who
			// is on call.
			s.setRosterError(err)
			groups = append(groups, group)
			continue
		}
		if name := strings.TrimSpace(oncall.Name); name != "" {
			group.Watchers = []*v1.Watcher{NewWatcher(ctx, name, s.resolve)}
		}
		groups = append(groups, group)
	}
	return groups, nil
}
