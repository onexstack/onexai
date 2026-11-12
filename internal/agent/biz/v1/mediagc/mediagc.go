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

// Package mediagc reclaims stored images whose deadline has passed.
//
// # Why a deadline exists at all
//
// Every image the bot receives is copied out of the chat platform and into this
// service's own storage, and nothing else ever removes it. Without a collector
// the bucket is a permanent record of every screenshot anyone ever sent — which
// is a cost that grows without bound and a retention question nobody asked for.
//
// # Why it is off until somebody turns it on
//
// The deadline is stamped by the upload from a configured retention, and the
// default retention is "never". Deleting a user's picture is not recoverable,
// so the machinery waits for an operator to say how long images are kept rather
// than picking a number that empties a conversation somebody still reads.
//
// # What it is not
//
// It is not a reconciliation. It deletes what the table says is due, and it
// deliberately does not sweep the store for objects no row mentions: a measured
// deployment showed zero of those, and a pass that deletes things nothing is
// complaining about is a pass that can only be wrong.
package mediagc

import (
	"context"
	"log/slog"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/pkg/media"
)

// Defaults.
const (
	// DefaultInterval is how often a pass runs. An hour, because the thing being
	// measured is a retention of days: a tighter schedule would scan a table for
	// no reason, and a looser one makes "expired" a word that means "eventually".
	DefaultInterval = time.Hour
	// DefaultBatch bounds one pass, so a first run against a backlog reclaims in
	// several passes rather than one long transaction.
	DefaultBatch = 200
)

// Options configures the collector.
type Options struct {
	// Enabled starts the sweep. Off unless an operator set a retention.
	Enabled bool
	// Interval is how often a pass runs. Zero uses DefaultInterval.
	Interval time.Duration
	// Batch bounds one pass. Zero uses DefaultBatch.
	Batch int
}

// Collector reclaims expired assets.
type Collector interface {
	// Run sweeps until ctx is done.
	Run(ctx context.Context)
	// SweepOnce runs one pass and reports how many assets it reclaimed.
	SweepOnce(ctx context.Context) (int, error)
}

type collector struct {
	rows   agentstore.AssetStore
	store  *media.Bundle
	logger *slog.Logger
	opt    Options
}

// New builds a collector, or nil when it is disabled.
//
// A nil result is the ordinary case and the caller starts it unconditionally,
// so the return type is the interface rather than the concrete struct: a typed
// nil would pass a `!= nil` guard and then panic on the first call.
func New(rows agentstore.AssetStore, store *media.Bundle, logger *slog.Logger, opt Options) Collector {
	if !opt.Enabled || rows == nil || !store.Configured() {
		return nil
	}
	if opt.Interval <= 0 {
		opt.Interval = DefaultInterval
	}
	if opt.Batch <= 0 {
		opt.Batch = DefaultBatch
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &collector{rows: rows, store: store, logger: logger, opt: opt}
}

// Run sweeps until ctx is done.
func (c *collector) Run(ctx context.Context) {
	if c == nil {
		return
	}
	ticker := time.NewTicker(c.opt.Interval)
	defer ticker.Stop()

	c.logger.Info("image collector started", "interval", c.opt.Interval.String(), "batch", c.opt.Batch)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := c.SweepOnce(ctx); err != nil {
				// Logged rather than returned: this runs for the life of the
				// process, and one failed pass is retried by the next.
				c.logger.WarnContext(ctx, "image collection failed", "reclaimed", n, "error", err)
			}
		}
	}
}

// SweepOnce reclaims one batch of expired assets.
//
// # The order is bytes first, then the row
//
// The row is the only thing that knows where the object is. Removing the row
// first would lose the address of anything the second step failed on, and the
// object would stay in the bucket for ever with nothing left to name it.
func (c *collector) SweepOnce(ctx context.Context) (int, error) {
	if c == nil {
		return 0, nil
	}
	now := time.Now()
	due, err := c.rows.ListExpired(ctx, now, c.opt.Batch)
	if err != nil {
		return 0, err
	}

	reclaimed := 0
	for _, row := range due {
		if ctx.Err() != nil {
			return reclaimed, ctx.Err()
		}
		if err := c.reclaim(ctx, row); err != nil {
			// One asset's failure must not stop the pass: the rows are
			// independent, and an object the store refuses to delete would
			// otherwise block every asset behind it for ever.
			c.logger.WarnContext(ctx, "could not reclaim an expired image",
				"asset_id", row.ID, "object_key", row.ObjectKey, "error", err)
			continue
		}
		reclaimed++
	}
	return reclaimed, nil
}

// reclaim removes one asset's bytes and then its row.
func (c *collector) reclaim(ctx context.Context, row *model.AssetM) error {
	if row == nil || row.ID == "" {
		return nil
	}
	if err := c.store.Delete(ctx, media.Ref{ObjectKey: row.ObjectKey}); err != nil {
		return err
	}
	if err := c.rows.Delete(ctx, where.F("id", row.ID)); err != nil {
		// The bytes are gone and the row survives, which is the one state the
		// reader reports as a 404 rather than as a picture. The next pass tries
		// the delete again, and object deletion is idempotent, so the retry
		// costs nothing.
		return err
	}
	c.logger.InfoContext(ctx, "reclaimed an expired image", "asset_id", row.ID, "size", row.Size)
	return nil
}
