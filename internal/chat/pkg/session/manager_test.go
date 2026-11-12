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
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testManager(t *testing.T) (*Manager, *fakeClock) {
	t.Helper()

	clock := newFakeClock()
	m := NewManager(context.Background(), Options{
		DataDir:           t.TempDir(),
		MaxQueuedMessages: 2,
		IdleTimeout:       15 * time.Minute,
		TurnTimeout:       5 * time.Minute,
		StaleLockMaxHeld:  2 * time.Minute,
		Now:               clock.Now,
	})
	t.Cleanup(m.Close)
	return m, clock
}

func TestSessionCreatesAndDefaultsState(t *testing.T) {
	m, clock := testManager(t)

	s, err := m.Session("oc_abc123")
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}

	st := s.State()
	if st.Key != "oc_abc123" {
		t.Errorf("state key = %q, want oc_abc123", st.Key)
	}
	if st.Backend != BackendLLM {
		t.Errorf("state backend = %q, want %q (the default)", st.Backend, BackendLLM)
	}
	if st.Platform != PlatformLark {
		t.Errorf("state platform = %q, want %q", st.Platform, PlatformLark)
	}
	if !st.CreatedAt.Equal(clock.Now()) {
		t.Errorf("state created_at = %v, want %v", st.CreatedAt, clock.Now())
	}

	// A freshly created session has not been written to disk; nothing has
	// changed yet, so there is nothing worth a file.
	if _, err := os.Stat(filepath.Join(m.Directory(), FileName("oc_abc123"))); !os.IsNotExist(err) {
		t.Errorf("a fresh session wrote a snapshot before any state change (stat err = %v)", err)
	}
}

func TestSessionIsSharedPerKey(t *testing.T) {
	m, _ := testManager(t)

	first, err := m.Session("oc_a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Session("oc_a")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("Session returned two different Session values for the same key; they would not share a lock")
	}

	other, err := m.Session("oc_b")
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Error("Session returned the same Session for two different keys")
	}
}

func TestSessionRejectsEmptyKey(t *testing.T) {
	m, _ := testManager(t)
	if _, err := m.Session(""); err == nil {
		t.Error("Session(\"\") returned no error")
	}
}

// TestLockNotPersistedAcrossRestart is the test for the rule that makes the
// lock safe: a lock describes a running goroutine, so it must not outlive the
// process. If it were persisted, a restart mid-turn would come back with a
// conversation that can never accept another message.
func TestLockNotPersistedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	opts := Options{DataDir: dir, MaxQueuedMessages: 2, Now: newFakeClock().Now}

	first := NewManager(ctx, opts)
	s, err := first.Session("oc_abc123")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *State) {
		st.Backend = BackendCLI
		st.BackendSessionID = "sess-abc-123"
	}); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	// Take the lock and never release it, simulating a process that dies
	// mid-turn.
	if _, ok := s.Lock().TryLock("turn-1"); !ok {
		t.Fatal("TryLock failed on an unlocked Lock")
	}
	if !s.Busy() {
		t.Fatal("Busy() = false after TryLock")
	}

	// A new Manager over the same directory is a restart.
	second := NewManager(ctx, opts)
	t.Cleanup(second.Close)

	restarted, err := second.Session("oc_abc123")
	if err != nil {
		t.Fatalf("Session after restart returned error: %v", err)
	}
	if restarted.Busy() {
		t.Error("the restarted session came back busy; a lock outlived its process and now nothing can release it")
	}

	// The persisted binding, however, must survive.
	st := restarted.State()
	if st.Backend != BackendCLI {
		t.Errorf("restarted backend = %q, want %q (the binding must persist)", st.Backend, BackendCLI)
	}
	if st.BackendSessionID != "sess-abc-123" {
		t.Errorf("restarted backend session id = %q, want sess-abc-123", st.BackendSessionID)
	}
}

func TestCorruptSnapshotIsDiscardedOnLoad(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(context.Background(), Options{DataDir: dir, Now: newFakeClock().Now})
	t.Cleanup(m.Close)

	key := "oc_abc123"
	path := filepath.Join(dir, FileName(key))
	if err := os.WriteFile(path, []byte(`{"key":"oc_abc123","backend":`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := m.Session(key)
	if err != nil {
		t.Fatalf("Session returned an error for a corrupt snapshot; it must recover instead: %v", err)
	}
	if got := s.State().Backend; got != BackendLLM {
		t.Errorf("recovered backend = %q, want the default %q", got, BackendLLM)
	}
	// The damaged file is removed so it stops being reported on every load.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the corrupt snapshot was not removed (stat err = %v)", err)
	}
}

func TestRecoverAfterRestartFlagsCLISessions(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(context.Background(), Options{DataDir: dir, Now: newFakeClock().Now})

	// One LLM-backed conversation and one CLI-backed conversation.
	for _, tc := range []struct{ key, backend string }{
		{"oc_llm", BackendLLM},
		{"oc_cli", BackendCLI},
	} {
		s, err := m.Session(tc.key)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Update(func(st *State) { st.Backend = tc.backend }); err != nil {
			t.Fatal(err)
		}
	}

	recovered, err := m.RecoverAfterRestart(context.Background())
	if err != nil {
		t.Fatalf("RecoverAfterRestart returned error: %v", err)
	}
	if len(recovered) != 2 {
		t.Fatalf("RecoverAfterRestart returned %d conversations, want 2", len(recovered))
	}

	notices := map[string]bool{}
	for _, r := range recovered {
		notices[r.State.Key] = r.NeedsRestartNotice
	}
	if !notices["oc_cli"] {
		t.Error("a CLI-backed conversation needs a restart notice: its agent process died with the previous run")
	}
	if notices["oc_llm"] {
		t.Error("an LLM-backed conversation needs no restart notice: its state lives in agent, which outlived the restart")
	}
}

// waitFor polls cond until it holds or the deadline passes. Used to observe a
// background goroutine without sleeping for a fixed period.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// TestRunReaperReapsThenClosesOnShutdown covers both halves of the reaper's
// lifecycle: it must retire idle conversations while running, and it must not
// leave any behind when the process shuts down.
func TestRunReaperReapsThenClosesOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(ctx, Options{DataDir: t.TempDir(), IdleTimeout: 20 * time.Millisecond})

	reaped, err := m.Session("oc_reaped")
	if err != nil {
		t.Fatal(err)
	}
	go m.RunReaper(ctx)

	if !waitFor(reaped.Closed) {
		t.Fatal("the reaper did not close a conversation idle for longer than the idle timeout")
	}

	survivor, err := m.Session("oc_survivor")
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	if !waitFor(survivor.Closed) {
		t.Fatal("shutdown left a conversation open")
	}
}

// TestRunReaperWithoutIdleTimeoutReturns pins that a disabled idle timeout makes
// the reaper a no-op rather than a busy loop.
func TestRunReaperWithoutIdleTimeoutReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		NewManager(context.Background(), Options{DataDir: t.TempDir()}).RunReaper(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunReaper did not return with no idle timeout configured")
	}
}

func TestReapClosesIdleSessionsOnly(t *testing.T) {
	clock := newFakeClock()
	m := NewManager(context.Background(), Options{
		DataDir:     t.TempDir(),
		IdleTimeout: 15 * time.Minute,
		Now:         clock.Now,
	})
	t.Cleanup(m.Close)

	idle, err := m.Session("oc_idle")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := m.Session("oc_fresh")
	if err != nil {
		t.Fatal(err)
	}

	// Age the idle one, then make the fresh one active again.
	clock.Advance(30 * time.Minute)
	if err := fresh.Touch(); err != nil {
		t.Fatal(err)
	}
	// A session with a turn in flight is not idle, whatever its timestamp.
	busy, err := m.Session("oc_busy")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := busy.Lock().TryLock("turn-1"); !ok {
		t.Fatal("TryLock failed")
	}

	closed := m.Reap()

	if len(closed) != 1 || closed[0] != "oc_idle" {
		t.Errorf("Reap closed %v, want exactly [oc_idle]", closed)
	}
	if !idle.Closed() {
		t.Error("the idle session was reported closed but is not")
	}
	if fresh.Closed() {
		t.Error("a recently touched session was closed")
	}
	if busy.Closed() {
		t.Error("a session with a turn in flight was closed")
	}
}

// TestSessionSubmitSecondMessageQueues is the behaviour behind fixing a user
// seeing a red card when they send a second message mid-turn: the second
// message waits its turn, and runs after the first rather than concurrently.
func TestSessionSubmitSecondMessageQueues(t *testing.T) {
	m, _ := testManager(t)
	s, err := m.Session("oc_abc123")
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu    sync.Mutex
		order []string
	)
	record := func(name string) TurnFunc {
		return func(context.Context, uint64) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	// Block the first turn so the second is genuinely queued behind it.
	release := make(chan struct{})
	firstRan := make(chan struct{})
	first := func(context.Context, uint64) error {
		mu.Lock()
		order = append(order, "first")
		mu.Unlock()
		close(firstRan)
		<-release
		return nil
	}

	type result struct {
		queued bool
		err    error
	}
	firstDone := make(chan result, 1)
	go func() {
		queued, err := s.Submit(context.Background(), first)
		firstDone <- result{queued, err}
	}()
	<-firstRan

	// Second message while the first runs: queued, not run, no error.
	queued, err := s.Submit(context.Background(), record("second"))
	if err != nil {
		t.Fatalf("second Submit returned error: %v", err)
	}
	if !queued {
		t.Error("second Submit reported queued=false while a turn was running")
	}
	if s.QueueDepth() != 1 {
		t.Errorf("QueueDepth() = %d, want 1", s.QueueDepth())
	}

	// Third message: still admitted (MaxQueuedMessages is 2).
	if _, err := s.Submit(context.Background(), record("third")); err != nil {
		t.Fatalf("third Submit returned error: %v", err)
	}
	// Fourth message: the queue is full.
	if _, err := s.Submit(context.Background(), record("fourth")); !errors.Is(err, ErrQueueFull) {
		t.Errorf("fourth Submit error = %v, want ErrQueueFull", err)
	}

	close(release)
	if got := <-firstDone; got.queued || got.err != nil {
		t.Errorf("first Submit = (queued=%v, err=%v), want (false, nil)", got.queued, got.err)
	}

	// The queued turns run in submission order, before Submit returns.
	mu.Lock()
	defer mu.Unlock()
	want := []string{"first", "second", "third"}
	if len(order) != len(want) {
		t.Fatalf("turns ran in order %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("turns ran in order %v, want %v (queued turns must not overtake each other)", order, want)
		}
	}
	if s.QueueDepth() != 0 {
		t.Errorf("QueueDepth() = %d after draining, want 0", s.QueueDepth())
	}
}

func TestSessionSubmitRejectsWhenClosed(t *testing.T) {
	m, _ := testManager(t)
	s, err := m.Session("oc_abc123")
	if err != nil {
		t.Fatal(err)
	}

	s.Close()
	if !s.Closed() {
		t.Fatal("Closed() = false after Close()")
	}

	// Hold the lock so the submit goes down the enqueue path, which is the one
	// Close guards; the inline path needs no queue.
	if _, ok := s.Lock().TryLock("turn-1"); !ok {
		t.Fatal("TryLock failed")
	}
	if _, err := s.Submit(context.Background(), func(context.Context, uint64) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("Submit on a closed session = %v, want ErrClosed", err)
	}
}

// TestSubmitReleasesLockOnPanic covers the failure that would be worst in
// production: a panicking turn that keeps the lock and locks the conversation
// out permanently, with no way for the user to recover.
func TestSubmitReleasesLockOnPanic(t *testing.T) {
	m, _ := testManager(t)
	s, err := m.Session("oc_abc123")
	if err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("a panicking turn did not propagate its panic to the caller")
			}
		}()
		_, _ = s.Submit(context.Background(), func(context.Context, uint64) error {
			panic("turn exploded")
		})
	}()

	if s.Busy() {
		t.Error("the session is still busy after a panicking turn; it can never accept another message")
	}
	if _, err := s.Submit(context.Background(), func(context.Context, uint64) error { return nil }); err != nil {
		t.Errorf("Submit after a panicking turn returned error: %v", err)
	}
}

// TestForceUnlockDiscardsQueue pins that stopping a run stops the conversation
// rather than letting it chew through the messages that piled up behind it.
func TestForceUnlockDiscardsQueue(t *testing.T) {
	m, _ := testManager(t)
	s, err := m.Session("oc_abc123")
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		queued int
	)
	release := make(chan struct{})
	firstRan := make(chan struct{})

	go func() {
		_, _ = s.Submit(context.Background(), func(_ context.Context, gen uint64) error {
			close(firstRan)
			<-release
			// The turn outlives its own lock: the user pressed stop.
			if s.Lock().Generation() == gen {
				t.Error("the lock generation did not change after ForceUnlock")
			}
			return nil
		})
	}()
	<-firstRan

	for range 2 {
		if _, err := s.Submit(context.Background(), func(context.Context, uint64) error {
			mu.Lock()
			queued++
			mu.Unlock()
			return nil
		}); err != nil {
			t.Fatalf("Submit returned error while queueing: %v", err)
		}
	}

	s.Lock().ForceUnlock()
	close(release)

	// Give the draining goroutine a chance to finish; it stops as soon as it
	// notices the lock is gone.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !s.Busy() && s.QueueDepth() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if queued != 0 {
		t.Errorf("%d queued turns ran after the session was force-stopped, want 0", queued)
	}
}

// TestConcurrentSubmitsRunTurnsSerially would catch a broken admission path as
// overlapping turns under -race, and as a mis-ordered count here.
func TestConcurrentSubmitsRunTurnsSerially(t *testing.T) {
	m, _ := testManager(t)
	s, err := m.Session("oc_abc123")
	if err != nil {
		t.Fatal(err)
	}

	const attempts = 32
	var (
		mu      sync.Mutex
		running int
		overlap int
		ran     int
	)

	var wg sync.WaitGroup
	wg.Add(attempts)
	for range attempts {
		go func() {
			defer wg.Done()
			// Errors are expected: the queue is deliberately small.
			_, _ = s.Submit(context.Background(), func(context.Context, uint64) error {
				mu.Lock()
				running++
				if running > 1 {
					overlap++
				}
				ran++
				mu.Unlock()

				time.Sleep(time.Millisecond)

				mu.Lock()
				running--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()

	if overlap != 0 {
		t.Errorf("%d turns overlapped; turns for one conversation must run one at a time", overlap)
	}
	if ran == 0 {
		t.Error("no turn ran at all")
	}
	mu.Lock()
	defer mu.Unlock()
	if running != 0 {
		t.Errorf("%d turns are still marked running after all submits returned", running)
	}
}

// TestConcurrentTurnsRunUpToCapacity is the fix this package exists around:
// with room for more than one turn, a conversation answers several questions at
// once and queues only what arrives beyond that. A capacity of 1 is covered by
// TestConcurrentSubmitsRunTurnsSerially above.
func TestConcurrentTurnsRunUpToCapacity(t *testing.T) {
	s, err := NewManager(context.Background(), Options{
		DataDir:            t.TempDir(),
		MaxQueuedMessages:  4,
		MaxConcurrentTurns: 2,
		IdleTimeout:        15 * time.Minute,
		TurnTimeout:        5 * time.Minute,
	}).Session("oc_abc123")
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan int, 4)
	release := make(chan struct{})
	turn := func(id int) TurnFunc {
		return func(context.Context, uint64) error {
			started <- id
			<-release
			return nil
		}
	}

	// Two turns take the two slots and block there.
	var wg sync.WaitGroup
	for id := range 2 {
		wg.Go(func() {
			if queued, err := s.Submit(context.Background(), turn(id)); err != nil || queued {
				t.Errorf("turn %d: queued=%v err=%v, want a slot straight away", id, queued, err)
			}
		})
	}

	// Both must be inside their turn before the third is submitted, or the
	// third would race the ones above for the free slots.
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("fewer than two turns started; the capacity was not two")
		}
	}

	// With both slots taken the third has nowhere to run: it queues.
	queued, err := s.Submit(context.Background(), turn(2))
	if err != nil {
		t.Fatalf("third Submit returned error: %v", err)
	}
	if !queued {
		t.Error("the third turn ran instead of queueing; the capacity is not enforced")
	}
	if got := s.QueueDepth(); got != 1 {
		t.Errorf("QueueDepth() = %d, want 1", got)
	}

	// Freeing the slots lets the queued turn run.
	close(release)
	wg.Wait()
	select {
	case id := <-started:
		if id != 2 {
			t.Errorf("the turn that ran after a slot freed was %d, want the queued 2", id)
		}
	case <-time.After(2 * time.Second):
		t.Error("the queued turn never ran after a slot was freed")
	}
}

// TestEveryAdmittedTurnEventuallyRuns pins the boundary between "every slot is
// taken" and "the queue went empty". A turn Submit accepted — by running it or
// by queueing it — must run. A queued turn that no running turn ever drains is
// invisible until a user notices they were told "排队中" (queued) and then never answered,
// which is the failure this guards.
func TestEveryAdmittedTurnEventuallyRuns(t *testing.T) {
	s, err := NewManager(context.Background(), Options{
		DataDir:            t.TempDir(),
		MaxQueuedMessages:  8,
		MaxConcurrentTurns: 1,
		IdleTimeout:        15 * time.Minute,
		TurnTimeout:        5 * time.Minute,
	}).Session("oc_abc123")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	var (
		admitted atomic.Int64
		ran      atomic.Int64
	)

	var wg sync.WaitGroup
	for range 300 {
		// A burst of concurrent submissions lands on both sides of the instant
		// the running turn finishes and frees its slot.
		for range 4 {
			wg.Go(func() {
				if _, err := s.Submit(ctx, func(context.Context, uint64) error {
					ran.Add(1)
					return nil
				}); err == nil {
					// Accepted: either it ran inline, or it is queued and must run
					// before the conversation goes quiet.
					admitted.Add(1)
				}
			})
		}
	}
	wg.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ran.Load() < admitted.Load() {
		time.Sleep(2 * time.Millisecond)
	}
	if got, want := ran.Load(), admitted.Load(); got != want {
		t.Errorf("only %d of %d admitted turns ran; %d were stranded in the queue", got, want, want-got)
	}
}
