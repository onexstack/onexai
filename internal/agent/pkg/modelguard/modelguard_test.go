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

package modelguard

import (
	"errors"
	"testing"
	"time"
)

// newTestGuard returns a guard whose clock is a variable the test moves.
func newTestGuard(t *testing.T, cfg Config) (*Guard, *time.Time) {
	t.Helper()
	g := New(cfg)
	if g == nil {
		t.Fatal("New returned nil for a configured guard")
	}
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	return g, &now
}

func TestNewDisabled(t *testing.T) {
	for _, cfg := range []Config{{}, {QPS: 0, FailureThreshold: 0}} {
		if g := New(cfg); g != nil {
			t.Fatalf("New(%+v) = %v, want nil for a disabled guard", cfg, g)
		}
	}
	var g *Guard
	if g.Enabled() {
		t.Fatal("a nil guard reports itself enabled")
	}
	if err := g.Allow("m"); err != nil {
		t.Fatalf("a nil guard refused a call: %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	g, now := newTestGuard(t, Config{QPS: 1, Burst: 1})

	if err := g.Allow("m"); err != nil {
		t.Fatalf("the first call was refused: %v", err)
	}
	if err := g.Allow("m"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("the second immediate call returned %v, want ErrRateLimited", err)
	}
	// Another endpoint has its own bucket.
	if err := g.Allow("other"); err != nil {
		t.Fatalf("a different endpoint was refused: %v", err)
	}
	// One second later the bucket has refilled.
	*now = now.Add(time.Second)
	if err := g.Allow("m"); err != nil {
		t.Fatalf("a call after refill was refused: %v", err)
	}
}

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	g, now := newTestGuard(t, Config{FailureThreshold: 2, Cooldown: 10 * time.Second})

	g.Record("m", errors.New("boom"))
	if err := g.Allow("m"); err != nil {
		t.Fatalf("one failure must not open the breaker: %v", err)
	}
	g.Record("m", errors.New("boom"))
	if err := g.Allow("m"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("after the threshold the breaker returned %v, want ErrCircuitOpen", err)
	}

	// After the cooldown one probe is allowed.
	*now = now.Add(10 * time.Second)
	if err := g.Allow("m"); err != nil {
		t.Fatalf("the probe was refused: %v", err)
	}
	// A failing probe reopens immediately rather than needing the full streak.
	g.Record("m", errors.New("still broken"))
	if err := g.Allow("m"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("a failed probe left the breaker closed: %v", err)
	}
}

func TestBreakerSuccessClosesIt(t *testing.T) {
	g, _ := newTestGuard(t, Config{FailureThreshold: 2, Cooldown: time.Minute})

	g.Record("m", errors.New("boom"))
	g.Record("m", nil) // a success clears the streak
	g.Record("m", errors.New("boom"))
	if err := g.Allow("m"); err != nil {
		t.Fatalf("the streak was not reset by the success: %v", err)
	}
}

func TestBreakersArePerEndpoint(t *testing.T) {
	g, _ := newTestGuard(t, Config{FailureThreshold: 1, Cooldown: time.Minute})

	g.Record("bad", errors.New("boom"))
	if err := g.Allow("bad"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("the failing endpoint was allowed: %v", err)
	}
	if err := g.Allow("good"); err != nil {
		t.Fatalf("a healthy endpoint was refused because another failed: %v", err)
	}
}
