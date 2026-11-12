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

// newTestLimiter returns a limiter over a real (in-process) Redis and the day
// its clock should be read as.
func newTestLimiter(t *testing.T, limit int) (*Limiter, *redis.Client) {
	t.Helper()
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	l := New(client, "onexai", limit)
	l.now = func() time.Time { return time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC) }
	return l, client
}

// TestAllow_CountsUpToTheLimitAndThenRefuses is the behaviour the quota exists
// for: the first N requests of the day pass, the next one does not.
func TestAllow_CountsUpToTheLimitAndThenRefuses(t *testing.T) {
	const limit = 3
	l, _ := newTestLimiter(t, limit)
	ctx := context.Background()

	for i := 1; i <= limit; i++ {
		ok, remaining, err := l.Allow(ctx, "go.kong")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("request %d was refused although the quota is %d", i, limit)
		}
		if want := limit - i; remaining != want {
			t.Errorf("request %d: remaining = %d, want %d", i, remaining, want)
		}
	}

	ok, remaining, err := l.Allow(ctx, "go.kong")
	if err != nil {
		t.Fatalf("over-limit request: %v", err)
	}
	if ok {
		t.Fatal("the request past the quota was allowed")
	}
	if remaining != 0 {
		t.Errorf("remaining = %d, want 0", remaining)
	}
}

// TestAllow_RefusalDoesNotInflateTheCounter pins the refund. Without it every
// refused request raises the count, and the expiry the counter carries is then
// computed from a number that no longer means "requests today".
func TestAllow_RefusalDoesNotInflateTheCounter(t *testing.T) {
	const limit = 2
	l, client := newTestLimiter(t, limit)
	ctx := context.Background()

	for i := 0; i < limit+10; i++ {
		if _, _, err := l.Allow(ctx, "go.kong"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	count, err := client.Get(ctx, l.key("go.kong")).Int()
	if err != nil {
		t.Fatalf("reading the counter: %v", err)
	}
	if count != limit {
		t.Errorf("counter = %d after %d attempts, want it pinned at the limit %d", count, limit+10, limit)
	}
}

// TestAllow_CountersArePerSender pins that one person's usage is not another's.
func TestAllow_CountersArePerSender(t *testing.T) {
	const limit = 2
	l, _ := newTestLimiter(t, limit)
	ctx := context.Background()

	for i := 0; i < limit; i++ {
		if _, _, err := l.Allow(ctx, "go.kong"); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _, _ := l.Allow(ctx, "go.kong"); ok {
		t.Fatal("go.kong went over the quota")
	}
	if ok, remaining, err := l.Allow(ctx, "someone.else"); !ok || err != nil {
		t.Fatalf("a different sender was refused by go.kong's usage: (%v, %v)", ok, err)
	} else if remaining != limit-1 {
		t.Errorf("remaining = %d, want %d", remaining, limit-1)
	}
}

// TestAllow_AnonymousIsNotCounted pins that a request with no sender is not
// charged to a shared bucket: counting every anonymous caller under the empty
// string would give them one quota between them, which is worse than a
// per-sender quota and worse than none.
func TestAllow_AnonymousIsNotCounted(t *testing.T) {
	const limit = 1
	l, client := newTestLimiter(t, limit)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if ok, _, err := l.Allow(ctx, ""); !ok || err != nil {
			t.Fatalf("an anonymous request was refused: (%v, %v)", ok, err)
		}
	}

	count, err := client.Get(ctx, l.key("")).Int()
	if err == nil && count > 0 {
		t.Errorf("anonymous requests were counted (%d); there is nothing to attribute them to", count)
	}
}

// TestAllow_ExpiresWithTheDay pins that yesterday's usage does not follow the
// sender into today.
func TestAllow_ExpiresWithTheDay(t *testing.T) {
	const limit = 1
	l, _ := newTestLimiter(t, limit)
	ctx := context.Background()

	if ok, _, err := l.Allow(ctx, "go.kong"); !ok || err != nil {
		t.Fatalf("first request of the day: (%v, %v)", ok, err)
	}
	if ok, _, _ := l.Allow(ctx, "go.kong"); ok {
		t.Fatal("the quota was not enforced")
	}

	// The next day is a different key, so the quota starts over.
	l.now = func() time.Time { return time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC) }
	if ok, _, err := l.Allow(ctx, "go.kong"); !ok || err != nil {
		t.Fatalf("the first request of the next day was refused: (%v, %v)", ok, err)
	}
}

// TestAllow_RedisFailureAllows pins the fail-open stance: the quota protects
// capacity, and turning a cache outage into an outage of the product is a
// strictly worse trade.
func TestAllow_RedisFailureAllows(t *testing.T) {
	// An address nothing listens on, with retries off so the failure is quick.
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 200 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })

	l := New(client, "onexai", 1)
	ok, _, err := l.Allow(context.Background(), "go.kong")
	if !ok {
		t.Fatal("a request was refused because Redis was unreachable")
	}
	if err == nil {
		t.Error("the failure was not reported; the caller logs it, and silence would hide an outage")
	}
}

func TestEnabled(t *testing.T) {
	var nilLimiter *Limiter
	if nilLimiter.Enabled() {
		t.Error("a nil limiter is not enabled")
	}
	if New(nil, "p", 50).Enabled() {
		t.Error("a limiter with no store is not enabled")
	}
	l, _ := newTestLimiter(t, 5)
	if !l.Enabled() {
		t.Error("a configured limiter must report enabled")
	}
}
