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
	"sync"
	"time"
)

// DefaultFlushInterval and DefaultBatchSize bound how long one write waits and
// how many it carries.
const (
	DefaultFlushInterval = 50 * time.Millisecond
	DefaultBatchSize     = 500

	// flushTimeout bounds one statement group. The caller's context deliberately
	// does not: once a row is accepted into the batch the write must finish even
	// if the reconcile that offered it was cancelled, otherwise a shutdown or a
	// retry storm could silently drop updates.
	flushTimeout = 30 * time.Second
)

// batchOp is what a queued item asks for.
type batchOp int

const (
	opUpsert batchOp = iota
	opDelete
	opStatus
)

// item is one write waiting for its batch.
type item struct {
	op   batchOp
	row  Row
	key  Key
	done chan error
}

// Batcher coalesces per-object writes into bounded statements.
//
// A reconciler is inherently per-key: the first list of a million objects would
// otherwise be a million statements. Coalescing turns that into about two
// thousand, without changing the caller's contract — Upsert returns only after
// the batch carrying that row has been written, so a returned error still means
// "not persisted".
type Batcher struct {
	writer   Writer
	interval time.Duration
	maxItems int

	queue chan *item
	quit  chan struct{}
	once  sync.Once
}

// NewBatcher starts a batcher. Close stops it.
func NewBatcher(writer Writer, size int, interval time.Duration) *Batcher {
	if size <= 0 {
		size = DefaultBatchSize
	}
	if interval <= 0 {
		interval = DefaultFlushInterval
	}

	b := &Batcher{
		writer:   writer,
		interval: interval,
		maxItems: size,
		queue:    make(chan *item, size),
		quit:     make(chan struct{}),
	}
	go b.loop()
	return b
}

// Upsert queues rows and returns once they are persisted.
func (b *Batcher) Upsert(ctx context.Context, rows []Row) error {
	return b.submitRows(ctx, rows, opUpsert)
}

// StatusOnly queues status refreshes and returns once they are persisted.
func (b *Batcher) StatusOnly(ctx context.Context, rows []Row) error {
	return b.submitRows(ctx, rows, opStatus)
}

// Delete queues deletions and returns once they are persisted.
func (b *Batcher) Delete(ctx context.Context, keys []Key) error {
	return b.submitKeys(ctx, keys)
}

func (b *Batcher) submitRows(ctx context.Context, rows []Row, op batchOp) error {
	for i := range rows {
		if err := b.wait(ctx, &item{op: op, row: rows[i], done: make(chan error, 1)}); err != nil {
			return err
		}
	}
	return nil
}

func (b *Batcher) submitKeys(ctx context.Context, keys []Key) error {
	for i := range keys {
		if err := b.wait(ctx, &item{op: opDelete, key: keys[i], done: make(chan error, 1)}); err != nil {
			return err
		}
	}
	return nil
}

func (b *Batcher) wait(ctx context.Context, it *item) error {
	select {
	case b.queue <- it:
	case <-ctx.Done():
		return ctx.Err()
	case <-b.quit:
		return context.Canceled
	}

	select {
	case err := <-it.done:
		return err
	case <-ctx.Done():
		// The item is already queued and will be written; reporting the caller's
		// cancellation is honest about this call, and the write is not lost.
		return ctx.Err()
	}
}

// Close flushes what is queued and stops the loop.
func (b *Batcher) Close() {
	b.once.Do(func() { close(b.quit) })
}

func (b *Batcher) loop() {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	pending := make([]*item, 0, b.maxItems)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		batch := pending
		pending = make([]*item, 0, b.maxItems)
		b.flush(batch)
	}

	for {
		select {
		case <-b.quit:
			// Drain whatever the queue still holds before reporting the rows as
			// failed; a shutdown should not turn a healthy batch into an error.
			for {
				select {
				case it := <-b.queue:
					pending = append(pending, it)
					if len(pending) >= b.maxItems {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case it := <-b.queue:
			pending = append(pending, it)
			if len(pending) >= b.maxItems {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// flush writes one batch, grouped by operation so a mixed batch still costs one
// statement per operation instead of one per row.
func (b *Batcher) flush(batch []*item) {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()

	var (
		upserts  []Row
		statuses []Row
		deletes  []Key
	)
	for _, it := range batch {
		switch it.op {
		case opUpsert:
			upserts = append(upserts, it.row)
		case opStatus:
			statuses = append(statuses, it.row)
		case opDelete:
			deletes = append(deletes, it.key)
		}
	}

	var err error
	if len(upserts) > 0 {
		err = b.writer.Upsert(ctx, upserts)
		countWrites(upserts, "upsert")
	}
	if err == nil && len(statuses) > 0 {
		err = b.writer.StatusOnly(ctx, statuses)
		countWrites(statuses, "status")
	}
	if err == nil && len(deletes) > 0 {
		err = b.writer.Delete(ctx, deletes)
		for _, key := range deletes {
			writesTotal.WithLabelValues(key.Kind, "delete").Inc()
		}
	}
	if err != nil {
		writeErrorsTotal.Inc()
	}

	for _, it := range batch {
		it.done <- err
	}
}

// countWrites attributes a batch to the kinds it carried.
func countWrites(list []Row, op string) {
	for i := range list {
		writesTotal.WithLabelValues(list[i].Kind, op).Inc()
	}
}
