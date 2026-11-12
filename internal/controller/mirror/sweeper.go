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

package mirror

import (
	"context"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/onexstack/onexai/pkg/readmodel"
)

// DefaultSweepInterval is how often the projection is verified.
const DefaultSweepInterval = 5 * time.Minute

// Sweeper is the second-level reconciliation.
//
// The reconciler only sees events; anything that happened while this process was
// down produces none. The sweeper compares the whole control plane against the
// projection and repairs both directions:
//
//   - an object the projection lacks, or whose spec drifted → written
//   - an object whose only status drifted → status refreshed, spec untouched
//   - a projected row whose object is gone → deleted
//
// One pass, no watermark writes: the in-memory index already knows every row,
// so the cost of a pass that finds nothing is a list plus a hash comparison.
type Sweeper struct {
	Client client.Client
	Writer Writer

	// Indexes is each kind's in-memory picture, shared with that kind's
	// reconciler so a change written by an event is never mistaken for staleness.
	Indexes map[string]*index
	Kinds   []Kind

	Interval time.Duration
	// Namespaces mirrors the cache scope; empty means every namespace.
	Namespaces []string

	lastSuccess time.Time
}

// Start runs the sweep immediately and then on the interval.
//
// The first pass runs at once rather than after one interval: it is what
// publishes the watermarks the read side gates on, and waiting would leave every
// mirror-backed read falling back for five minutes after each restart.
func (s *Sweeper) Start(ctx context.Context) error {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultSweepInterval
	}

	s.sweep(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

// NeedLeaderElection implements manager.LeaderElectionRunnable. Deleting rows is
// destructive, so only the leader sweeps.
func (s *Sweeper) NeedLeaderElection() bool { return true }

// sweep verifies every projected kind once.
func (s *Sweeper) sweep(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx)

	for _, kind := range s.Kinds {
		if ctx.Err() != nil {
			return
		}
		if err := s.sweepKind(ctx, kind); err != nil {
			log.Error(err, "failed to verify the projection", "kind", kind.Kind)
			if !s.lastSuccess.IsZero() {
				lagSeconds.WithLabelValues(kind.Kind).Set(time.Since(s.lastSuccess).Seconds())
			}
			continue
		}
		lagSeconds.WithLabelValues(kind.Kind).Set(0)
	}
	s.lastSuccess = time.Now()
}

func (s *Sweeper) sweepKind(ctx context.Context, kind Kind) error {
	log := ctrl.LoggerFrom(ctx)
	kindIndex := s.Indexes[kind.Kind]
	passStart := time.Now()

	list := kind.NewList()
	// The objects are only read, never mutated, so skipping the cache's defensive
	// deep copy keeps a large kind's pass from doubling the process's object
	// retention.
	if err := s.Client.List(ctx, list, client.UnsafeDisableDeepCopy); err != nil {
		return err
	}
	objects, err := apimeta.ExtractList(list)
	if err != nil {
		return err
	}

	var (
		upserts   []Row
		statuses  []Row
		projected int
	)
	for _, raw := range objects {
		obj, ok := raw.(client.Object)
		if !ok {
			continue
		}
		if kind.Exclude != nil && kind.Exclude(obj) {
			continue
		}
		projected++
		row, err := Project(kind, obj, passStart)
		if err != nil {
			log.Error(err, "failed to project an object", "kind", kind.Kind, "name", obj.GetName())
			continue
		}

		previous, known := kindIndex.get(row.Namespace, row.Name)
		switch {
		case !known || previous.spec != row.SpecHash:
			// Drifted or never written: the event that should have carried this
			// was lost, or it predates this process.
			upserts = append(upserts, row)
			kindIndex.put(row.Namespace, row.Name, row.SpecHash, row.StatusHash, passStart)
		case previous.status != row.StatusHash:
			// Status-only drift is the normal case for a healthy control plane:
			// the controllers patch status on their own timers.
			statuses = append(statuses, row)
			kindIndex.put(row.Namespace, row.Name, row.SpecHash, row.StatusHash, passStart)
		default:
			kindIndex.touch(row.Namespace, row.Name, passStart)
		}
	}

	if err := s.write(ctx, upserts, statuses); err != nil {
		return err
	}

	// The guard sees everything the API returned; the watermark counts only what
	// the projection actually holds, so an excluded object is not reported as a
	// projected row.
	if err := s.deleteStale(ctx, kind, kindIndex, passStart, len(objects)); err != nil {
		return err
	}

	rows.WithLabelValues(kind.Kind).Set(float64(kindIndex.len()))
	return s.Writer.PutWatermark(ctx, readmodel.Watermark{
		Group:       kind.Group,
		Kind:        kind.Kind,
		ObjectCount: int64(projected),
		LastSweepAt: passStart,
		UpdatedAt:   time.Now(),
	})
}

func (s *Sweeper) write(ctx context.Context, upserts, statuses []Row) error {
	if len(upserts) > 0 {
		if err := s.Writer.Upsert(ctx, upserts); err != nil {
			return err
		}
		countWrites(upserts, "sweep-upsert")
	}
	if len(statuses) > 0 {
		if err := s.Writer.StatusOnly(ctx, statuses); err != nil {
			return err
		}
		countWrites(statuses, "sweep-status")
	}
	return nil
}

// deleteStale removes rows whose objects the pass did not see.
//
// The guard is the whole safety story: a kind that lists as empty while the
// projection holds rows is far more likely to mean the cache lost visibility
// (RBAC withdrew, the scope narrowed, the API server is failing) than that every
// object of that kind was deleted. Skipping costs one stale interval; deleting
// wrongly costs a rebuild.
func (s *Sweeper) deleteStale(
	ctx context.Context, kind Kind, kindIndex *index, passStart time.Time, seen int,
) error {
	stale := kindIndex.stale(passStart)
	if len(stale) == 0 {
		return nil
	}
	if seen == 0 {
		sweepSkippedTotal.WithLabelValues(kind.Kind, "empty-listing").Inc()
		ctrl.LoggerFrom(ctx).Info(
			"skipping stale-row deletion: the kind listed as empty while the projection holds rows",
			"kind", kind.Kind, "projected", kindIndex.len())
		return nil
	}

	keys := make([]Key, 0, len(stale))
	for _, key := range stale {
		key.Group = kind.Group
		key.Kind = kind.Kind
		keys = append(keys, key)
		kindIndex.forget(key.Namespace, key.Name)
	}
	if err := s.Writer.Delete(ctx, keys); err != nil {
		return err
	}
	sweepDeletedTotal.WithLabelValues(kind.Kind).Add(float64(len(keys)))
	return nil
}
