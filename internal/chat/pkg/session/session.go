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
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

var (
	// ErrQueueFull reports that a conversation already has the maximum number
	// of turns waiting and cannot accept another. The caller is expected to tell
	// the user to retry rather than to retry on their behalf.
	ErrQueueFull = errors.New("session turn queue is full")
	// ErrClosed reports a turn submitted to a conversation that has been closed.
	ErrClosed = errors.New("session is closed")
)

// TurnFunc is one unit of work belonging to a conversation. The generation is
// the lock generation the turn runs under; a turn that outlives its own lock
// can compare it against Lock.Generation to discover it was broken.
type TurnFunc func(ctx context.Context, gen uint64) error

// Session is one conversation: its persisted state plus the rule bounding how
// many turns run at once.
//
// Turns are admitted up to maxConcurrent (see Options.MaxConcurrentTurns) and the
// overflow is queued rather than dropped. With the default capacity of 1 a second
// message that arrives mid-turn is queued and runs afterwards, because silently
// dropping it (or, worse, running it concurrently against the same streaming
// card) is not something a user can see or understand. A larger capacity runs up
// to that many turns side by side — each with its own card, since each turn draws
// its own — and queues only what arrives beyond them. Either way the queue is
// bounded; past that the user is told the conversation is busy, which is at least
// a visible answer.
type Session struct {
	key   string
	store Store
	lock  *Lock
	opts  Options

	mu     sync.Mutex
	state  State
	turns  chan TurnFunc
	closed bool
	// pending is the approval this conversation is blocked on, if any. It is
	// never written to the snapshot — see PendingPermission.
	pending *PendingPermission
	// currentRun is the run this conversation is executing, if any. In memory
	// only, and never written to the snapshot — see run.go.
	currentRun string

	// ctx is the process-lifetime context queued turns run under. A queued turn
	// cannot use its submitter's context: that request returned long ago, and
	// its cancellation says nothing about whether the turn should still run.
	ctx context.Context
}

func newSession(ctx context.Context, key string, st State, opts Options, store Store) *Session {
	maxQueued := max(opts.MaxQueuedMessages, 0)
	return &Session{
		key:   key,
		store: store,
		lock:  NewLock(opts.MaxConcurrentTurns, opts.Now),
		opts:  opts,
		state: st,
		turns: make(chan TurnFunc, maxQueued),
		ctx:   ctx,
	}
}

// Key returns the conversation key.
func (s *Session) Key() string { return s.key }

// Lock exposes the busy lock so a caller can break a stale hold or force-stop a
// running turn.
func (s *Session) Lock() *Lock { return s.lock }

// Busy reports whether a turn is currently running.
func (s *Session) Busy() bool { return s.lock.Busy() }

// QueueDepth returns how many turns are waiting behind the running one.
func (s *Session) QueueDepth() int { return len(s.turns) }

// State returns a copy of the persisted state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Clone()
}

// Update applies fn to the state and persists the result. A persistence failure
// is returned but does not roll back the in-memory change: the running turn
// should not fail because a snapshot could not be written, and the next Update
// will try again.
func (s *Session) Update(fn func(*State)) error {
	s.mu.Lock()
	if fn != nil {
		fn(&s.state)
	}
	snapshot := s.state.Clone()
	s.mu.Unlock()

	if err := s.store.Save(snapshot); err != nil {
		slog.Warn("failed to persist session snapshot", "session", s.key, "error", err)
		return err
	}
	return nil
}

// Touch records user activity, refreshing the idle timer.
func (s *Session) Touch() error {
	return s.Update(func(st *State) { st.LastUserActivity = s.now() })
}

// Submit runs fn as this conversation's next turn.
//
// It reports queued=true when every concurrency slot was already taken and fn
// was placed in the queue instead of starting now; the caller uses that to tell
// the user their message is waiting. When the queue is full it returns
// ErrQueueFull and fn never runs.
//
// A turn that panics still releases the lock (the release is deferred), so a
// panicking turn cannot leave the conversation permanently locked out. The
// turns still queued in that case are left for the next Submit to drain, since
// the panic will already have been reported to the user by the caller's own
// recovery.
func (s *Session) Submit(ctx context.Context, fn TurnFunc) (queued bool, err error) {
	// Admission and enqueue happen under the same mutex the drain loop uses to
	// decide it has nothing left to run. Without that they could interleave: a
	// submitter could see every slot taken just as the last running turn saw an
	// empty queue and went to free its slot, and the turn would sit in the queue
	// with nobody left to drain it. Holding s.mu across both decisions makes them
	// one: whoever runs second sees the other's result.
	s.mu.Lock()
	gen, ok := s.lock.Acquire(s.key)
	if !ok {
		err := s.enqueueLocked(fn)
		s.mu.Unlock()
		return true, err
	}
	s.mu.Unlock()

	// The slot is released in runTurns once the queue is drained, but a panic in
	// fn would skip that; this covers it. Release forgets a generation it no
	// longer holds, so running here after a normal exit is a no-op.
	defer func() {
		s.mu.Lock()
		s.lock.Release(gen)
		s.mu.Unlock()
	}()

	s.runTurns(ctx, gen, fn)
	return false, nil
}

// runTurns runs fn under the already-held slot, then drains anything queued
// while it ran, so the queued turns execute in submission order rather than each
// waiting for a new message to wake them. A session with more than one slot has
// one such loop per running turn; each keeps its own slot while it drains, so
// the number of loops — and therefore of concurrent turns — stays within the
// lock's capacity.
func (s *Session) runTurns(ctx context.Context, gen uint64, first TurnFunc) {
	_ = first(ctx, gen)

	for {
		s.mu.Lock()
		if !s.lock.Holds(gen) {
			// The hold was broken while a turn was running — a user pressed stop,
			// or a watchdog broke a wedged turn. Whoever asked for that did not
			// ask to then chew through the queue, so drop it. When several turns
			// were broken at once only the first to reach here finds anything.
			n := s.discardQueuedLocked()
			s.mu.Unlock()
			if n > 0 {
				slog.Info("discarded queued turns after the session lock was broken",
					"session", s.key, "discarded", n)
			}
			return
		}
		next, ok := s.dequeueLocked()
		if !ok {
			// Nothing left to run. Free the slot here, in the same critical
			// section that saw the queue empty, so a Submit finding every slot
			// taken cannot race this exit and strand its turn (see Submit).
			s.lock.Release(gen)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		// Queued turns run under the process context, not the submitter's: the
		// request that queued this turn has long since returned.
		_ = next(s.ctx, gen)
	}
}

// enqueueLocked appends fn to the queue. The caller must hold s.mu.
func (s *Session) enqueueLocked(fn TurnFunc) error {
	if s.closed {
		return ErrClosed
	}
	select {
	case s.turns <- fn:
		return nil
	default:
		return ErrQueueFull
	}
}

// dequeueLocked pops the next queued turn. The caller must hold s.mu.
func (s *Session) dequeueLocked() (TurnFunc, bool) {
	select {
	case fn := <-s.turns:
		return fn, true
	default:
		return nil, false
	}
}

// discardQueuedLocked empties the queue and reports how many turns it dropped.
// The caller must hold s.mu.
func (s *Session) discardQueuedLocked() int {
	n := 0
	for {
		select {
		case <-s.turns:
			n++
		default:
			return n
		}
	}
}

// Close stops the conversation accepting further turns. A turn already running
// is not interrupted: its own timeout, or an explicit lock break, ends it.
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// Closed reports whether the session has been closed.
func (s *Session) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// IdleFor returns how long since the last recorded user activity.
func (s *Session) IdleFor() time.Duration {
	st := s.State()
	if st.LastUserActivity.IsZero() {
		return 0
	}
	return s.now().Sub(st.LastUserActivity)
}

func (s *Session) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}
