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

// Package dispatcher resumes admitted asynchronous runs. A run submitted through
// POST /v1/agent/runs is executed immediately by the handler; this loop is the
// recovery path. It finds admissions that are due but still pending, and
// admissions whose running owner stopped renewing its lease, and claims them.
package dispatcher

import (
	"context"
	"log/slog"
	"time"

	"github.com/onexstack/onexai/internal/agent/store/runstore"
	"github.com/onexstack/onexai/internal/pkg/safego"
)

// DefaultInterval is how often the dispatcher looks for pending admissions.
const DefaultInterval = 30 * time.Second

// DefaultBatch bounds one sweep so a backlog cannot spawn unbounded goroutines.
const DefaultBatch = 100

// RunDispatcher resumes pending run admissions.
type RunDispatcher struct {
	runs     runstore.Store
	execute  func(ctx context.Context, id string)
	interval time.Duration
	batch    int
}

// New returns a dispatcher over the run store. execute is the same function the
// HTTP handler calls, so a resumed run and a fresh one take one path.
func New(runs runstore.Store, execute func(context.Context, string), interval time.Duration, batch int) *RunDispatcher {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if batch <= 0 {
		batch = DefaultBatch
	}
	return &RunDispatcher{runs: runs, execute: execute, interval: interval, batch: batch}
}

// Run blocks until ctx is done.
func (d *RunDispatcher) Run(ctx context.Context) error {
	if d == nil || d.runs == nil || d.execute == nil {
		return nil
	}
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			d.sweep(ctx)
		}
	}
}

func (d *RunDispatcher) sweep(ctx context.Context) {
	pending, err := d.runs.ListPendingAdmissions(ctx, d.batch)
	if err != nil {
		slog.WarnContext(ctx, "run dispatcher: list pending admissions", "error", err)
		return
	}
	for _, admission := range pending {
		id := admission.ID
		safego.Go(ctx, "run dispatcher: "+id, func() { d.execute(ctx, id) })
	}
}
