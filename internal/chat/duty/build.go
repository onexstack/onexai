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
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/oncall"
	"github.com/onexstack/onexai/internal/chat/userdir"
	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// BuildGroup converts a group row into the command-facing Group, without
// evaluating the rotation. ScheduleSource is "regen" when the row still names
// a legacy schedule and "none" otherwise; ApplyNativeRoster overwrites it.
//
// It is shared by the periodic legacy source and the live duty service so the
// two cannot drift in how they map a row (agents, order, descriptions,
// allocation strategy) onto a command result.
func BuildGroup(m *model.OncallGroupM) Group {
	if m == nil {
		return Group{}
	}
	id := strings.ToLower(strings.TrimSpace(m.Name))
	name := strings.TrimSpace(m.DisplayName)
	if name == "" {
		name = m.Name
	}
	source := "none"
	scheduleID := strings.TrimSpace(m.ScheduleID)
	if scheduleID != "" {
		source = "regen"
	}
	return Group{
		ID:                 id,
		Name:               name,
		RawName:            m.Name,
		Namespace:          m.Namespace,
		ScheduleID:         scheduleID,
		ShortDesc:          m.ShortDesc,
		LongDesc:           m.LongDesc,
		WelcomeMessage:     m.WelcomeMessage,
		Agents:             m.AgentList(),
		Owner:              m.Owner,
		Order:              m.Order,
		AllocationStrategy: m.AllocationStrategy,
		ScheduleSource:     source,
	}
}

// ApplyNativeRoster evaluates a native schedule aggregate at now and fills the
// group's watchers and primary metadata.
func ApplyNativeRoster(
	ctx context.Context, g *Group, agg *model.OncallScheduleAggregate, now time.Time, resolve userdir.Resolver,
) {
	if g == nil || agg == nil || agg.Schedule == nil {
		return
	}
	g.ScheduleSource = "native"
	watchers := oncall.WatchersAt(agg, now)
	g.Watchers = make([]*v1.Watcher, 0, len(watchers))
	for _, current := range watchers {
		if strings.TrimSpace(current.UserName) == "" {
			continue
		}
		g.Watchers = append(g.Watchers, NewWatcher(ctx, current.UserName, resolve))
	}
	if len(watchers) == 0 {
		return
	}
	g.PrimaryIsOverride = watchers[0].IsOverride
	g.PrimaryLayerName = watchers[0].LayerName
	g.PrimaryUntil = watchers[0].Until
}

// NewWatcher maps an on-call name onto the Lark watcher the ticket group is
// created from. The name is resolved through the account directory, with the
// configured on-call map taking precedence. When neither knows the name the raw
// name is kept, which is what /assign does: a deployment that has not bound the
// person yet still gets a readable /duty list.
func NewWatcher(ctx context.Context, name string, resolve userdir.Resolver) *v1.Watcher {
	userID := ""
	if resolve != nil {
		userID = resolve.LarkUserID(ctx, name)
	}
	if userID == "" {
		slog.WarnContext(ctx, "duty: no Lark user_id resolved for the on-call user, using the raw name",
			"user", name)
		userID = name
	}
	return &v1.Watcher{UserId: userID, UserName: name}
}

// AssignStrategyOf maps the group's configured strategy to the proto enum.
// Unknown values fall back to primary, which is the pre-existing behaviour.
func AssignStrategyOf(strategy string) v1.AssignStrategy {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "primary_backup", "primary-backup", "primarybackup":
		return v1.AssignStrategy_ASSIGN_PRIMARY_BACKUP
	case "round_robin", "round-robin", "roundrobin":
		return v1.AssignStrategy_ASSIGN_ROUND_ROBIN
	case "time_based", "time-based", "timebased":
		return v1.AssignStrategy_ASSIGN_TIME_BASED
	default:
		return v1.AssignStrategy_ASSIGN_PRIMARY
	}
}
