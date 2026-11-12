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
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Options configures a Manager and the Sessions it hands out.
type Options struct {
	// DataDir is the directory holding conversation snapshots.
	DataDir string
	// MaxQueuedMessages bounds how many turns may wait behind a running one.
	MaxQueuedMessages int
	// MaxConcurrentTurns is how many turns may run at once in one conversation.
	// 1 serialises turns (the overflow queues, as it always has); N>1 runs up to
	// N side by side and queues only what arrives beyond them. A value below 1
	// is treated as 1.
	MaxConcurrentTurns int
	// IdleTimeout is how long a conversation may sit untouched before it is
	// considered reapable.
	IdleTimeout time.Duration
	// TurnTimeout is the intended upper bound on a turn, recorded on the
	// session for callers to apply.
	TurnTimeout time.Duration
	// StaleLockMaxHeld is how long a turn may hold the busy lock before
	// BreakStale releases it.
	StaleLockMaxHeld time.Duration
	// DefaultBackend is the backend a new conversation binds to.
	DefaultBackend string
	// Platform is the platform recorded on new conversations.
	Platform string
	// Now is the clock, injectable for tests.
	Now func() time.Time
	// OnClosed, when set, is called for every conversation that leaves the
	// manager — reaped as idle, or closed with the process.
	//
	// It exists because a conversation is not only this package's bookkeeping: a
	// backend may hold a process for it, and a conversation that is forgotten
	// without telling that backend leaves the process behind. Called outside the
	// manager's lock, and its result is not waited on.
	OnClosed func(key string)
}

func (o Options) withDefaults() Options {
	o.MaxQueuedMessages = max(o.MaxQueuedMessages, 0)
	o.MaxConcurrentTurns = max(o.MaxConcurrentTurns, 1)
	if o.DefaultBackend == "" {
		o.DefaultBackend = BackendLLM
	}
	if o.Platform == "" {
		o.Platform = PlatformLark
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Manager owns the live conversations and their snapshots.
//
// It is deliberately not backed by the database store. Conversation *history*
// lives in oai-agent-apiserver; what lives here is a small, single-writer binding
// that is worth nothing after a restart that cannot resume the process it
// describes. Keeping it out of the shared store also keeps this package free of
// a global database handle, so two Managers can coexist in one process — which
// is what makes it testable without a database.
type Manager struct {
	opts  Options
	store Store
	ctx   context.Context

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager creates a Manager. ctx is the process-lifetime context queued turns
// run under; it must outlive the conversation being queued.
func NewManager(ctx context.Context, opts Options) *Manager {
	opts = opts.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}
	return &Manager{
		opts:     opts,
		store:    NewDiskStore(opts.DataDir),
		ctx:      ctx,
		sessions: make(map[string]*Session),
	}
}

// TurnTimeout returns the configured bound on a single turn, or 0 when unset.
func (m *Manager) TurnTimeout() time.Duration { return m.opts.TurnTimeout }

// RunReaper closes idle conversations until ctx is done.
//
// Reaping is what keeps the session map bounded: it is keyed by conversation and
// would otherwise hold every conversation the bot has ever replied in, for the
// life of the process. The scan runs at half the idle timeout (capped at a
// minute) so a conversation is closed within roughly one timeout of falling
// idle, and a small map is scanned often enough that the cost is irrelevant.
func (m *Manager) RunReaper(ctx context.Context) {
	interval := m.opts.IdleTimeout / 2
	if interval <= 0 {
		return
	}
	interval = min(interval, time.Minute)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.Close()
			return
		case <-ticker.C:
			m.Reap()
		}
	}
}

// Store returns the snapshot store, exposed so a caller can pre-load or
// inspect snapshots without going through a session.
func (m *Manager) Store() Store { return m.store }

// Directory returns the snapshot directory.
func (m *Manager) Directory() string { return m.opts.DataDir }

// Session returns the conversation for key, loading its snapshot from disk or
// creating a fresh one.
//
// A snapshot that exists but cannot be decoded is discarded rather than
// propagated: it is a convenience file, and refusing to serve a conversation
// because its binding is damaged would turn a small problem into an outage. The
// damaged file is removed so it stops being reported.
func (m *Manager) Session(key string) (*Session, error) {
	if key == "" {
		return nil, errors.New("session key must not be empty")
	}

	m.mu.Lock()
	if s, ok := m.sessions[key]; ok {
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()

	st, found, err := m.store.Load(key)
	if err != nil {
		if !errors.Is(err, ErrCorrupt) {
			return nil, err
		}
		slog.Warn("discarding corrupt session snapshot", "session", key, "error", err)
		if derr := m.store.Delete(key); derr != nil {
			slog.Warn("failed to remove corrupt session snapshot", "session", key, "error", derr)
		}
		found = false
	}
	if !found {
		now := m.opts.Now()
		st = State{
			Key:              key,
			Platform:         m.opts.Platform,
			Backend:          m.opts.DefaultBackend,
			LastUserActivity: now,
			CreatedAt:        now,
		}
	}

	s := newSession(m.ctx, key, st, m.opts, m.store)

	m.mu.Lock()
	defer m.mu.Unlock()
	// Another goroutine may have created the same session while this one was
	// reading from disk; the first into the map wins so all callers share one
	// lock.
	if existing, ok := m.sessions[key]; ok {
		return existing, nil
	}
	m.sessions[key] = s
	return s, nil
}

// Lookup returns an already-loaded conversation without touching disk.
func (m *Manager) Lookup(key string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key]
	return s, ok
}

// Sessions returns the live conversations.
func (m *Manager) Sessions() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// Recovered describes a conversation found on disk at startup.
type Recovered struct {
	// State is the snapshot that was read.
	State State
	// NeedsRestartNotice is true for a CLI-backed conversation. Its agent ran as
	// a child process of the previous run, so that process is gone and the
	// conversation cannot be resumed where it stopped; the user has to be told
	// to start a new one rather than left waiting for an answer that will never
	// arrive.
	NeedsRestartNotice bool
}

// RecoverAfterRestart reads every snapshot and reports which conversations lost
// their backend to the restart.
//
// It does not delete or rewrite anything: the caller decides what to tell the
// users, and a snapshot that is merely stale is still the only record of which
// backend the conversation used.
func (m *Manager) RecoverAfterRestart(_ context.Context) ([]Recovered, error) {
	states, err := m.store.List()
	if err != nil {
		return nil, fmt.Errorf("list session snapshots: %w", err)
	}

	recovered := make([]Recovered, 0, len(states))
	for _, st := range states {
		recovered = append(recovered, Recovered{
			State: st,
			// Only the CLI backend owns a process. The LLM backend's state lives
			// in agent, which outlives this restart.
			NeedsRestartNotice: st.Backend == BackendCLI,
		})
	}
	slog.Info("recovered session snapshots", "count", len(recovered))
	return recovered, nil
}

// Reap closes conversations idle for longer than the configured timeout and
// returns the keys it closed. A conversation with a turn in flight is left
// alone even if idle: its lock is the evidence that something still cares.
func (m *Manager) Reap() []string {
	timeout := m.opts.IdleTimeout
	if timeout <= 0 {
		return nil
	}

	var closed []string
	m.mu.Lock()
	for key, s := range m.sessions {
		if s.Busy() || s.IdleFor() < timeout {
			continue
		}
		s.Close()
		delete(m.sessions, key)
		closed = append(closed, key)
	}
	m.mu.Unlock()

	// Announced after the lock is released: what a backend does with the news is
	// its own business, and a backend that called back into the manager would
	// deadlock against the map it is being removed from.
	m.announceClosed(closed)

	if len(closed) > 0 {
		slog.Info("reaped idle sessions", "count", len(closed), "idle_timeout", timeout)
	}
	return closed
}

// announceClosed tells the observer which conversations are gone.
func (m *Manager) announceClosed(keys []string) {
	if m.opts.OnClosed == nil {
		return
	}
	for _, key := range keys {
		m.opts.OnClosed(key)
	}
}

// Close closes every live conversation. Snapshots are not deleted: they are the
// record of the binding, and a restart is exactly when they are needed.
func (m *Manager) Close() {
	m.mu.Lock()
	keys := make([]string, 0, len(m.sessions))
	for key, s := range m.sessions {
		s.Close()
		keys = append(keys, key)
	}
	m.mu.Unlock()

	m.announceClosed(keys)
}
