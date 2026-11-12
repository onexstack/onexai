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
	"testing"
	"time"
)

// fakeClock is a manually advanced clock, so staleness can be tested without
// sleeping for the real duration.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestTryLockExcludes(t *testing.T) {
	l := NewLock(1, nil)

	gen, ok := l.TryLock("turn-1")
	if !ok {
		t.Fatal("first TryLock failed on an unlocked Lock")
	}
	if gen == 0 {
		t.Error("first TryLock returned generation 0; generations must be non-zero so a zero can mean 'not held'")
	}
	if !l.Busy() {
		t.Error("Busy() = false after a successful TryLock")
	}

	if secondGen, ok := l.TryLock("turn-2"); ok {
		t.Errorf("second TryLock succeeded with generation %d while the lock was held", secondGen)
	}
	if l.Holder() != "turn-1" {
		t.Errorf("Holder() = %q, want turn-1 (the second attempt must not take over)", l.Holder())
	}

	l.Release(gen)

	if l.Busy() {
		t.Error("Busy() = true after Release")
	}
	if _, ok := l.TryLock("turn-3"); !ok {
		t.Error("TryLock failed after the lock was released")
	}
}

// TestUnlockWrongGenerationIsNoop is the load-bearing test for the generation
// scheme. The sequence it models is the one that motivated generations: a turn
// wedges, its lock is broken, a retry takes the lock, and only then does the
// original turn finally return and try to release. A plain boolean lock would
// hand the conversation to two turns at once here.
func TestUnlockWrongGenerationIsNoop(t *testing.T) {
	clock := newFakeClock()
	l := NewLock(1, clock.Now)

	staleGen, ok := l.TryLock("wedged-turn")
	if !ok {
		t.Fatal("TryLock failed on an unlocked Lock")
	}

	// A watchdog breaks the wedged turn, then a retry takes the lock.
	clock.Advance(10 * time.Minute)
	if !l.BreakStale(2 * time.Minute) {
		t.Fatal("BreakStale did not fire on a turn held for 10m with a 2m threshold")
	}

	retryGen, ok := l.TryLock("retry-turn")
	if !ok {
		t.Fatal("TryLock failed after the wedged turn's lock was broken")
	}
	if retryGen == staleGen {
		t.Fatalf("retry acquired generation %d, the same generation as the wedged turn", retryGen)
	}

	// The wedged turn now finishes and releases with its stale generation.
	l.Release(staleGen)

	if !l.Busy() {
		t.Error("the wedged turn's Release freed the retry turn's lock; the retry is now unprotected")
	}
	if l.Holder() != "retry-turn" {
		t.Errorf("Holder() = %q, want retry-turn", l.Holder())
	}

	// And the retry's own release does work.
	l.Release(retryGen)
	if l.Busy() {
		t.Error("Busy() = true after the retry released its own generation")
	}
}

func TestBreakStaleAfterMaxHeld(t *testing.T) {
	clock := newFakeClock()
	l := NewLock(1, clock.Now)

	gen, ok := l.TryLock("wedged-turn")
	if !ok {
		t.Fatal("TryLock failed on an unlocked Lock")
	}

	// Not yet stale: a break must leave the lock alone.
	clock.Advance(90 * time.Second)
	if l.BreakStale(2 * time.Minute) {
		t.Error("BreakStale fired at 90s of a 2m threshold")
	}
	if !l.Busy() {
		t.Error("BreakStale released the lock even though it returned false")
	}

	// Stale: the break fires and the wedged turn's release becomes a no-op.
	clock.Advance(90 * time.Second)
	if !l.BreakStale(2 * time.Minute) {
		t.Fatal("BreakStale did not fire at 3m of a 2m threshold")
	}
	if l.Busy() {
		t.Error("Busy() = true after BreakStale fired")
	}

	l.Release(gen)
	newGen, ok := l.TryLock("retry")
	if !ok {
		t.Fatal("TryLock failed after BreakStale released the lock")
	}
	l.Release(gen) // the wedged turn's late release again
	if !l.Busy() {
		t.Error("the wedged turn's stale Release freed the retry turn's lock")
	}
	l.Release(newGen)
}

func TestBreakStaleOnUnlockedLockIsNoop(t *testing.T) {
	clock := newFakeClock()
	l := NewLock(1, clock.Now)

	clock.Advance(time.Hour)
	if l.BreakStale(time.Second) {
		t.Error("BreakStale reported a break on an unlocked Lock")
	}
}

func TestForceUnlock(t *testing.T) {
	l := NewLock(1, nil)

	gen, ok := l.TryLock("turn-1")
	if !ok {
		t.Fatal("TryLock failed on an unlocked Lock")
	}

	l.ForceUnlock()
	if l.Busy() {
		t.Fatal("Busy() = true after ForceUnlock")
	}
	if l.Holder() != "" {
		t.Errorf("Holder() = %q after ForceUnlock, want empty", l.Holder())
	}

	// The interrupted turn's release must not free whoever comes next.
	newGen, ok := l.TryLock("turn-2")
	if !ok {
		t.Fatal("TryLock failed after ForceUnlock")
	}
	if newGen == gen {
		t.Error("ForceUnlock did not advance the generation")
	}
	l.Release(gen)
	if !l.Busy() {
		t.Error("the interrupted turn's stale Release freed the new turn's lock")
	}
	l.Release(newGen)
}

func TestForceUnlockOnUnlockedLockIsNoop(t *testing.T) {
	l := NewLock(1, nil)
	before := l.Generation()

	l.ForceUnlock()

	if l.Busy() {
		t.Error("Busy() = true after ForceUnlock on an unlocked Lock")
	}
	if l.Generation() != before {
		t.Errorf("Generation() = %d after a no-op ForceUnlock, want %d", l.Generation(), before)
	}
}

func TestHeldFor(t *testing.T) {
	clock := newFakeClock()
	l := NewLock(1, clock.Now)

	if got := l.HeldFor(); got != 0 {
		t.Errorf("HeldFor() = %v on an unlocked Lock, want 0", got)
	}

	if _, ok := l.TryLock("turn-1"); !ok {
		t.Fatal("TryLock failed on an unlocked Lock")
	}
	clock.Advance(45 * time.Second)
	if got, want := l.HeldFor(), 45*time.Second; got != want {
		t.Errorf("HeldFor() = %v, want %v", got, want)
	}
}

// TestLockConcurrentAcquireExcludesOneWinner would surface a broken
// acquire/release pairing as a data race under -race, and as two winners here.
func TestLockConcurrentAcquireExcludesOneWinner(t *testing.T) {
	l := NewLock(1, nil)

	const contenders = 64
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
	)

	// Hold the lock so every contender must lose. Release only after they have
	// all tried, so the test measures exclusion rather than scheduling luck.
	gate := make(chan struct{})
	if _, ok := l.TryLock("holder"); !ok {
		t.Fatal("TryLock failed on an unlocked Lock")
	}

	wg.Add(contenders)
	for range contenders {
		go func() {
			defer wg.Done()
			<-gate
			if _, ok := l.TryLock("contender"); ok {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}

	close(gate)
	wg.Wait()

	if winners != 0 {
		t.Errorf("%d contenders acquired a held lock, want 0", winners)
	}
}

// TestLockAdmitsUpToCapacity pins the concurrency a session is allowed: exactly
// capacity turns run at once, the next waits, and freeing one slot lets it in.
func TestLockAdmitsUpToCapacity(t *testing.T) {
	l := NewLock(3, nil)
	if got := l.Capacity(); got != 3 {
		t.Fatalf("Capacity() = %d, want 3", got)
	}

	gens := make([]uint64, 0, 3)
	for i := range 3 {
		gen, ok := l.TryLock("turn")
		if !ok {
			t.Fatalf("TryLock #%d failed while fewer than capacity slots were held", i+1)
		}
		if !l.Holds(gen) {
			t.Errorf("Holds(%d) = false right after a successful acquire", gen)
		}
		gens = append(gens, gen)
	}

	if _, ok := l.TryLock("overflow"); ok {
		t.Error("TryLock succeeded past capacity; the overflow must queue rather than run")
	}

	// Freeing one slot admits the overflow; the other two keep theirs.
	l.Release(gens[0])
	if l.Holds(gens[0]) {
		t.Error("Holds() = true for a generation that was released")
	}
	if !l.Holds(gens[1]) || !l.Holds(gens[2]) {
		t.Error("releasing one generation dropped another live hold")
	}
	if _, ok := l.TryLock("overflow"); !ok {
		t.Error("TryLock failed after a slot was freed")
	}
}

// TestReleaseForgetsOnlyItsGeneration is the capacity>1 counterpart of the
// generation test above: a stale release must not free a slot it never held.
func TestReleaseForgetsOnlyItsGeneration(t *testing.T) {
	l := NewLock(2, nil)

	gen1, _ := l.TryLock("turn-1")
	gen2, _ := l.TryLock("turn-2")

	l.ForceUnlock()
	if l.Busy() {
		t.Fatal("Busy() = true after ForceUnlock")
	}
	if l.Holds(gen1) || l.Holds(gen2) {
		t.Error("ForceUnlock left a generation held")
	}

	// Both interrupted turns release their stale generations; neither may take
	// the fresh holds that follow.
	gen3, ok := l.TryLock("turn-3")
	if !ok {
		t.Fatal("TryLock failed after ForceUnlock")
	}
	l.Release(gen1)
	l.Release(gen2)
	if !l.Holds(gen3) {
		t.Error("a stale Release freed a fresh hold")
	}
}
