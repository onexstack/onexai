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

// Package sessiongc closes sessions that have been idle past their TTL. It
// runs in the agent service so it can clear the in-process/Redis conversation
// history and the durable session row in one place.
package sessiongc

import (
	"context"
	"log/slog"
	"time"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/store/runstore"
)

// Defaults applied when the deployment does not configure them.
const (
	DefaultSweep = time.Minute
	DefaultBatch = 100
)

// Collector closes idle sessions.
type Collector struct {
	Runs     runstore.Store
	Sessions mem.SessionStore
	TTL      time.Duration
	Interval time.Duration
	Batch    int
}

// Run sweeps until ctx is done.
func (c *Collector) Run(ctx context.Context) error {
	if c == nil || c.Runs == nil || c.TTL <= 0 {
		return nil
	}
	sweep := c.Interval
	if sweep <= 0 {
		sweep = DefaultSweep
	}
	// One sweep at startup catches what accumulated while the process was
	// down; the ticker then keeps the bound.
	c.sweepOnce(ctx)
	ticker := time.NewTicker(sweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.sweepOnce(ctx)
		}
	}
}

// Sweep runs one scan and reports how many sessions it closed.
func (c *Collector) Sweep(ctx context.Context) (int, error) {
	if c == nil || c.Runs == nil || c.TTL <= 0 {
		return 0, nil
	}
	batch := c.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	now := time.Now().UnixMilli()
	if _, err := c.Runs.ExpireApprovals(ctx, now); err != nil {
		slog.WarnContext(ctx, "idle session: expire approvals", "error", err)
	}
	before := time.Now().Add(-c.TTL).UnixMilli()
	sessions, err := c.Runs.ListIdleSessions(ctx, before, batch)
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, session := range sessions {
		if session == nil {
			continue
		}
		active, err := c.hasActiveRun(ctx, session.ID)
		if err != nil {
			slog.WarnContext(ctx, "idle session: list runs", "session", session.ID, "error", err)
			continue
		}
		if active {
			continue
		}
		if c.Sessions != nil {
			if err := c.Sessions.Clear(ctx, session.ID); err != nil {
				slog.WarnContext(ctx, "idle session: clear history", "session", session.ID, "error", err)
				continue
			}
		}
		if err := c.Runs.MarkSessionDeleted(ctx, session.ID, time.Now().UnixMilli()); err != nil {
			slog.WarnContext(ctx, "idle session: mark deleted", "session", session.ID, "error", err)
			continue
		}
		closed++
	}
	if closed > 0 {
		slog.InfoContext(ctx, "idle sessions closed", "count", closed, "ttl", c.TTL)
	}
	return closed, nil
}

func (c *Collector) sweepOnce(ctx context.Context) {
	if _, err := c.Sweep(ctx); err != nil {
		slog.WarnContext(ctx, "idle session sweep failed", "error", err)
	}
}

func (c *Collector) hasActiveRun(ctx context.Context, sessionID string) (bool, error) {
	runs, err := c.Runs.ListRun(ctx, sessionID, 0)
	if err != nil {
		return false, err
	}
	return hasActiveState(runs), nil
}

// hasActiveState reports whether any run is still executing or parked for a
// human. Such a session is not idle even if it has not been read recently.
func hasActiveState(runs []*model.RunM) bool {
	for _, run := range runs {
		if run == nil {
			continue
		}
		switch run.State {
		case model.RunStateRunning, model.RunStateInterrupted:
			return true
		}
	}
	return false
}
