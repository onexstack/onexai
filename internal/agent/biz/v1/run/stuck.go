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

package run

import (
	"context"
	"sort"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
	"github.com/onexstack/onexai/internal/pkg/errno"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

const (
	// stuckPendingTimeout is how long a due pending admission may wait before
	// it is considered stuck. Manual admissions have scheduled_time=0 and use
	// the same clock from their creation time.
	stuckPendingTimeout = 10 * time.Minute
	// stuckRunningTimeout is how long a run may keep running before it is
	// surfaced. A running admission whose lease expired is reported sooner:
	// nobody is renewing it, so it cannot make progress at all.
	stuckRunningTimeout = 60 * time.Minute
)

// AdminRunStuckThresholds echoes the fixed thresholds so the console can
// render "pending > 10m / running > 60m" without duplicating the constants.
type AdminRunStuckThresholds struct {
	PendingMs int64 `json:"pendingMs"`
	RunningMs int64 `json:"runningMs"`
}

// AdminRunStuckItem is one stuck run. Source distinguishes an asynchronous
// admission from an interactive run recorded straight into the run table.
type AdminRunStuckItem struct {
	ID          string `json:"id"`
	Agent       string `json:"agent"`
	State       string `json:"state"`
	Source      string `json:"source"`
	CreatedAt   int64  `json:"createdAt"`
	StartedAt   int64  `json:"startedAt"`
	ScheduledAt int64  `json:"scheduledAt"`
	LeaseUntil  int64  `json:"leaseUntil"`
	// Reason is pending_timeout, lease_expired or running_timeout.
	Reason string `json:"reason"`
}

// AdminRunStuckResponse is the stuck-run page.
type AdminRunStuckResponse struct {
	GeneratedAt int64                   `json:"generatedAt"`
	Thresholds  AdminRunStuckThresholds `json:"thresholds"`
	Items       []AdminRunStuckItem     `json:"items"`
}

// ListAdminRunStuck returns the runs that have stopped making progress.
//
// The store deliberately returns each source oldest first and capped at the
// requested limit; this layer merges them into one page ordered by the age
// anchor (admission creation time or interactive start time), so the oldest
// abandoned work is what an operator sees first.
func (b *runBiz) ListAdminRunStuck(
	ctx context.Context, req *v1.ListAdminRunStuckQuery,
) (*AdminRunStuckResponse, error) {
	if b.store == nil {
		return nil, errno.ErrNotFound.WithMessage("run store is not configured")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = v1.StuckDefaultLimit
	}
	if limit > v1.StuckMaxLimit {
		limit = v1.StuckMaxLimit
	}

	now := time.Now().UnixMilli()
	rows, err := b.store.ListStuckRuns(ctx, runstore.StuckRunFilter{
		Agent:         req.Agent,
		Now:           now,
		PendingBefore: now - stuckPendingTimeout.Milliseconds(),
		RunningBefore: now - stuckRunningTimeout.Milliseconds(),
		Limit:         limit,
	})
	if err != nil {
		return nil, errno.ErrOperationFailed.WithMessage("%s", err.Error())
	}

	items := make([]AdminRunStuckItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, AdminRunStuckItem{
			ID:          row.ID,
			Agent:       row.Agent,
			State:       row.State,
			Source:      row.Source,
			CreatedAt:   row.CreatedAt,
			StartedAt:   row.StartedAt,
			ScheduledAt: row.ScheduledTime,
			LeaseUntil:  row.LeaseUntil,
			Reason:      stuckReason(row, now),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		ai, aj := stuckAnchor(items[i]), stuckAnchor(items[j])
		if ai != aj {
			return ai < aj
		}
		return items[i].ID < items[j].ID
	})
	if len(items) > limit {
		items = items[:limit]
	}

	return &AdminRunStuckResponse{
		GeneratedAt: now,
		Thresholds: AdminRunStuckThresholds{
			PendingMs: stuckPendingTimeout.Milliseconds(),
			RunningMs: stuckRunningTimeout.Milliseconds(),
		},
		Items: items,
	}, nil
}

// stuckReason explains why a row is in the list. A pending row can only be
// waiting too long; a running admission may also have lost its lease.
func stuckReason(row runstore.StuckRunRow, now int64) string {
	switch {
	case row.State == model.RunStatePending:
		return "pending_timeout"
	case row.LeaseUntil > 0 && row.LeaseUntil < now:
		return "lease_expired"
	default:
		return "running_timeout"
	}
}

// stuckAnchor is the age anchor used to order the merged page.
func stuckAnchor(item AdminRunStuckItem) int64 {
	if item.Source == "run" {
		return item.StartedAt
	}
	return item.CreatedAt
}
