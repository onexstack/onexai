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

package session

import (
	"sync"
	"time"
)

// Lock is a per-conversation concurrency gate.
//
// It answers one question — "may one more turn start for this conversation?" —
// and the answer is yes while fewer than capacity turns are running. A capacity
// of 1 makes it the single-flight busy lock the bot started with: a second turn
// waits its turn. A larger capacity lets a conversation answer several questions
// at once, which is what the chat server's session.max-concurrent-turns
// configures.
//
// Every acquisition is tagged with a fresh generation, and the generations of
// the live holds are kept in a set. The generation is what makes a release safe:
// a turn that wedged, was broken by a watchdog or a user's stop, and only then
// returned must not free a slot it no longer owns. Releasing forgets exactly the
// generation it was handed, and only that one, so a stale release is a no-op
// rather than a mistake — and, unlike a single-generation flag, it cannot free
// another turn's slot out from under it.
//
// A Lock is not persisted and must not be: it describes goroutines that exist
// only in this process.
type Lock struct {
	mu       sync.Mutex
	capacity int
	// live maps each held generation to the instant it was acquired, so
	// staleness is measured per holder rather than for one lock-wide age.
	live   map[uint64]time.Time
	next   uint64
	holder string

	// now is the clock used for staleness, injectable so tests need not sleep.
	now func() time.Time
}

// NewLock creates an unlocked Lock that admits up to capacity concurrent
// holders. A capacity below 1 is treated as 1, so the zero value of the
// configuration still means "one turn at a time". A nil now uses time.Now.
func NewLock(capacity int, now func() time.Time) *Lock {
	if capacity < 1 {
		capacity = 1
	}
	if now == nil {
		now = time.Now
	}
	return &Lock{
		capacity: capacity,
		live:     make(map[uint64]time.Time, capacity),
		now:      now,
	}
}

// Capacity returns how many turns may hold the lock at once.
func (l *Lock) Capacity() int { return l.capacity }

// Acquire takes a free slot and returns the generation that must be passed back
// to Release. It reports false when every slot is taken, in which case the
// returned generation is zero and must not be used.
func (l *Lock) Acquire(holder string) (gen uint64, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acquireLocked(holder)
}

// TryLock is an alias for Acquire, kept because the call sites read as "try to
// start a turn" rather than "take a lock".
func (l *Lock) TryLock(holder string) (gen uint64, ok bool) { return l.Acquire(holder) }

func (l *Lock) acquireLocked(holder string) (uint64, bool) {
	if len(l.live) >= l.capacity {
		return 0, false
	}
	l.next++
	l.live[l.next] = l.now()
	l.holder = holder
	return l.next, true
}

// Release frees the slot named by gen. A generation that is not held — because
// the hold was broken and never re-taken, or because this release already ran —
// is ignored: the slot it named is no longer this caller's to free.
func (l *Lock) Release(gen uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked(gen)
}

// Unlock is an alias for Release.
func (l *Lock) Unlock(gen uint64) { l.Release(gen) }

func (l *Lock) releaseLocked(gen uint64) {
	delete(l.live, gen)
	if len(l.live) == 0 {
		l.holder = ""
	}
}

// Holds reports whether gen still names a live acquisition. A running turn uses
// it to discover that its hold was broken while it worked.
func (l *Lock) Holds(gen uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.live[gen]
	return ok
}

// BreakStale releases every hold older than maxHeld and reports whether it
// released any. This is the escape hatch for a turn that wedged — without it a
// single stuck turn would hold its slot forever, since the goroutine that holds
// the slot is exactly the one that cannot release it. Each broken generation is
// forgotten, so the wedged turn's eventual Release is a no-op.
func (l *Lock) BreakStale(maxHeld time.Duration) bool {
	if maxHeld <= 0 {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	broke := false
	for gen, heldAt := range l.live {
		if now.Sub(heldAt) >= maxHeld {
			delete(l.live, gen)
			broke = true
		}
	}
	if !broke {
		return false
	}
	// Advance the generation so any holder that was not broken sees its hold as
	// stale the next time it checks.
	l.next++
	if len(l.live) == 0 {
		l.holder = ""
	}
	return true
}

// ForceUnlock releases every hold unconditionally, advancing the generation. It
// is for an explicit user action ("stop this run"), where waiting for the
// staleness threshold would be the wrong behaviour.
func (l *Lock) ForceUnlock() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.live) == 0 {
		return
	}
	l.live = make(map[uint64]time.Time, l.capacity)
	l.holder = ""
	l.next++
}

// Busy reports whether any turn currently holds the lock.
func (l *Lock) Busy() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.live) > 0
}

// Holder returns the most recent holder's label, or "" when nothing is held.
func (l *Lock) Holder() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.holder
}

// HeldFor returns how long the longest-held slot has been held, or zero when
// nothing is held.
func (l *Lock) HeldFor() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.live) == 0 {
		return 0
	}
	longest := time.Duration(0)
	now := l.now()
	for _, heldAt := range l.live {
		if d := now.Sub(heldAt); d > longest {
			longest = d
		}
	}
	return longest
}

// Generation returns the current generation. It changes on every acquisition and
// on every break, so it can be used to detect that a hold was broken.
func (l *Lock) Generation() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.next
}
