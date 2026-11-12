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

package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestTokenLimiter returns a token limiter over a real (in-process) Redis and
// a day the clock is pinned to.
func newTestTokenLimiter(t *testing.T, limit int64) (*TokenLimiter, *redis.Client) {
	t.Helper()
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	l := NewTokenLimiter(client, "onexai", limit)
	l.now = func() time.Time { return time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC) }
	return l, client
}

// TestTokenAllow_IsTrueUntilTheCeilingIsReached is the behaviour the ceiling
// exists for, and it is deliberately late: a sender may spend up to the limit
// and is refused only once the day's total is at or past it, because what a run
// costs is not known until it has finished.
func TestTokenAllow_IsTrueUntilTheCeilingIsReached(t *testing.T) {
	const limit = 1000
	l, _ := newTestTokenLimiter(t, limit)
	ctx := context.Background()

	allowed, remaining, err := l.Allow(ctx, "go.kong")
	if err != nil {
		t.Fatalf("Allow on an untouched day: %v", err)
	}
	if !allowed || remaining != limit {
		t.Fatalf("Allow = (%v, %d), want (true, %d)", allowed, remaining, limit)
	}

	if err := l.Add(ctx, "go.kong", 600); err != nil {
		t.Fatalf("Add: %v", err)
	}
	allowed, remaining, err = l.Allow(ctx, "go.kong")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !allowed || remaining != 400 {
		t.Fatalf("Allow = (%v, %d), want (true, 400)", allowed, remaining)
	}

	if err := l.Add(ctx, "go.kong", 400); err != nil {
		t.Fatalf("Add: %v", err)
	}
	allowed, remaining, err = l.Allow(ctx, "go.kong")
	if err != nil {
		t.Fatalf("Allow at the ceiling: %v", err)
	}
	if allowed || remaining != 0 {
		t.Fatalf("Allow = (%v, %d), want (false, 0): the ceiling is reached", allowed, remaining)
	}
}

// TestTokenAdd_Accumulates pins that charges add up rather than replacing each
// other: a run's cost is one charge, and a day is many runs.
func TestTokenAdd_Accumulates(t *testing.T) {
	l, client := newTestTokenLimiter(t, 10000)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := l.Add(ctx, "go.kong", 100); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	used, err := client.Get(ctx, l.key("go.kong")).Int64()
	if err != nil {
		t.Fatalf("reading the counter: %v", err)
	}
	if used != 300 {
		t.Fatalf("counter = %d, want 300", used)
	}
}

// TestTokenAdd_NonPositiveWritesNothing pins that a run which spent nothing
// leaves no trace: writing 0 would create the day's key for a sender who has
// not spent anything, which is a counter that outlives its own meaning.
func TestTokenAdd_NonPositiveWritesNothing(t *testing.T) {
	l, client := newTestTokenLimiter(t, 1000)
	ctx := context.Background()

	for _, n := range []int64{0, -5} {
		if err := l.Add(ctx, "go.kong", n); err != nil {
			t.Fatalf("Add(%d): %v", n, err)
		}
	}
	if _, err := client.Get(ctx, l.key("go.kong")).Result(); err != redis.Nil {
		t.Fatalf("the counter exists after charging nothing (err = %v)", err)
	}
}

// TestTokenLimiter_DayBoundaryResetsTheTotal pins the date in the key. The TTL
// alone would leave yesterday's total readable for a moment after midnight,
// during which yesterday's spend would be charged to today's ceiling.
func TestTokenLimiter_DayBoundaryResetsTheTotal(t *testing.T) {
	l, _ := newTestTokenLimiter(t, 1000)
	ctx := context.Background()

	if err := l.Add(ctx, "go.kong", 1000); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if allowed, _, _ := l.Allow(ctx, "go.kong"); allowed {
		t.Fatal("the sender was allowed past the ceiling on the same day")
	}

	l.now = func() time.Time { return time.Date(2026, 10, 11, 0, 30, 0, 0, time.UTC) }
	allowed, remaining, err := l.Allow(ctx, "go.kong")
	if err != nil {
		t.Fatalf("Allow on the next day: %v", err)
	}
	if !allowed || remaining != 1000 {
		t.Fatalf("Allow = (%v, %d), want (true, 1000): a new day is a new allowance", allowed, remaining)
	}
}

// TestTokenLimiter_NilAndAnonymous pins the two cases that must never refuse:
// an unconfigured ceiling, and a run with nobody to charge. Counting an empty
// sender would pool every anonymous caller into one shared ceiling.
func TestTokenLimiter_NilAndAnonymous(t *testing.T) {
	ctx := context.Background()

	var none *TokenLimiter
	if ok, _, err := none.Allow(ctx, "go.kong"); !ok || err != nil {
		t.Fatalf("a nil limiter must allow everything, got (%v, %v)", ok, err)
	}
	if err := none.Add(ctx, "go.kong", 100); err != nil {
		t.Fatalf("a nil limiter must accept a charge silently, got %v", err)
	}
	if none.Enabled() || none.Limit() != 0 {
		t.Fatal("a nil limiter must report itself as absent")
	}

	l, _ := newTestTokenLimiter(t, 1)
	if ok, remaining, err := l.Allow(ctx, ""); !ok || remaining != -1 || err != nil {
		t.Fatalf("an unidentified sender must be allowed, got (%v, %d, %v)", ok, remaining, err)
	}
	if err := l.Add(ctx, "", 99999); err != nil {
		t.Fatalf("charging an unidentified sender: %v", err)
	}
	if ok, _, _ := l.Allow(ctx, "someone-else"); !ok {
		t.Fatal("an anonymous charge must not consume another sender's allowance")
	}
}

// TestNewTokenLimiter_DisabledWhenUnusable pins the constructor's two off
// switches: a non-positive limit is "no ceiling", and without Redis there is
// nowhere to count — a per-process count would give every replica its own copy
// of the same person's allowance.
func TestNewTokenLimiter_DisabledWhenUnusable(t *testing.T) {
	if l := NewTokenLimiter(nil, "onexai", 1000); l != nil {
		t.Fatal("a limiter without Redis must be nil")
	}
	if l := NewTokenLimiter(&redis.Client{}, "onexai", 0); l != nil {
		t.Fatal("a zero ceiling must be nil")
	}
}

func TestDescribeTokens(t *testing.T) {
	tests := []struct {
		limit int64
		want  string
	}{
		{limit: 8000000, want: "今日 AI 用量已达上限（800 万 token），请明天再试"},
		{limit: 5000, want: "今日 AI 用量已达上限（5000 token），请明天再试"},
	}
	for _, tc := range tests {
		if got := DescribeTokens(tc.limit); got != tc.want {
			t.Errorf("DescribeTokens(%d) = %q, want %q", tc.limit, got, tc.want)
		}
	}
}
