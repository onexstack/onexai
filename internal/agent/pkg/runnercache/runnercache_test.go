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

package runnercache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
)

// countingBuild returns a BuildFunc that counts its calls and hands out a
// distinguishable runner per call.
func countingBuild(calls *atomic.Int64) BuildFunc {
	return func(context.Context, []string) (*adk.Runner, []string, error) {
		calls.Add(1)
		return &adk.Runner{}, []string{"tool"}, nil
	}
}

func TestKey(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{name: "order independent", in: []string{"b", "a"}, want: "a.b"},
		{name: "duplicates collapse", in: []string{"a", "a", "b"}, want: "a.b"},
		{name: "blank entries drop", in: []string{" a ", "", "  "}, want: "a"},
		{name: "empty set has its own key", in: nil, want: "__no_agents__"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Key(tc.in); got != tc.want {
				t.Errorf("Key(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// The same selection listed two ways must share one entry.
	if Key([]string{"bke-agent", "sre-agent"}) != Key([]string{"sre-agent", "bke-agent"}) {
		t.Error("the cache key depends on the order the agents were listed in")
	}
}

func TestGet_BuildsOnceAndReuses(t *testing.T) {
	var calls atomic.Int64
	c := New(countingBuild(&calls), time.Minute)
	ctx := context.Background()

	first, key1, err := c.Get(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	second, key2, err := c.Get(ctx, []string{"b", "a"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if first != second {
		t.Error("the same agent set must yield the same runner")
	}
	if key1 != key2 {
		t.Errorf("keys differ: %q vs %q", key1, key2)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("the builder ran %d times, want 1", got)
	}
}

func TestGet_DifferentSetsBuildSeparately(t *testing.T) {
	var calls atomic.Int64
	c := New(countingBuild(&calls), time.Minute)
	ctx := context.Background()

	if _, _, err := c.Get(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Get(ctx, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("the builder ran %d times, want 2", got)
	}
}

func TestGet_ExpiryRebuilds(t *testing.T) {
	var calls atomic.Int64
	c := New(countingBuild(&calls), 10*time.Millisecond)
	ctx := context.Background()

	if _, _, err := c.Get(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, _, err := c.Get(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("an expired entry must rebuild, builder ran %d times", got)
	}
}

// TestGet_PinnedEntrySurvivesExpiry is the property that keeps a paused run
// resumable: while a human's answer is outstanding, the graph the interrupt was
// recorded against must not be replaced.
func TestGet_PinnedEntrySurvivesExpiry(t *testing.T) {
	var calls atomic.Int64
	c := New(countingBuild(&calls), 10*time.Millisecond)
	ctx := context.Background()

	first, _, err := c.Get(ctx, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	c.Pin([]string{"a"}, time.Minute)

	time.Sleep(20 * time.Millisecond)
	stillFirst, _, err := c.Get(ctx, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if stillFirst != first {
		t.Error("a pinned entry was rebuilt while a paused run still depends on it")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("the builder ran %d times, want 1", got)
	}

	// Once the answer arrives the entry may be replaced again.
	c.Unpin([]string{"a"})
	if _, _, err := c.Get(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("an unpinned entry must be free to rebuild, builder ran %d times", got)
	}
}

func TestUnpin_IsSafeWhenNotPinned(t *testing.T) {
	c := New(countingBuild(&atomic.Int64{}), time.Minute)
	ctx := context.Background()
	if _, _, err := c.Get(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	c.Unpin([]string{"a"}) // never pinned
	c.Unpin([]string{"a"}) // twice
	c.Unpin([]string{"never-seen"})

	c.mu.Lock()
	pinned := c.entries[Key([]string{"a"})].pinnedUntil
	c.mu.Unlock()
	if !pinned.IsZero() {
		t.Errorf("an unpinned entry should have no deadline, got %v", pinned)
	}
}

// TestPin_DecaysOnItsOwn pins the property that keeps a never-answered pause
// from freezing an entry forever.
func TestPin_DecaysOnItsOwn(t *testing.T) {
	var calls atomic.Int64
	c := New(countingBuild(&calls), 10*time.Millisecond)
	ctx := context.Background()
	if _, _, err := c.Get(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	c.Pin([]string{"a"}, 30*time.Millisecond)

	time.Sleep(40 * time.Millisecond) // past both the TTL and the pin
	if _, _, err := c.Get(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("a pin that outlived its deadline must not freeze the entry; builder ran %d times", got)
	}
}

func TestPin_OnUncachedSetIsHarmless(t *testing.T) {
	c := New(countingBuild(&atomic.Int64{}), time.Minute)
	c.Pin([]string{"never-built"}, time.Minute) // must not panic
	c.Unpin([]string{"never-built"})
}

func TestPin_ZeroDurationIsIgnored(t *testing.T) {
	var calls atomic.Int64
	c := New(countingBuild(&calls), 10*time.Millisecond)
	ctx := context.Background()
	if _, _, err := c.Get(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	c.Pin([]string{"a"}, 0)
	time.Sleep(20 * time.Millisecond)
	if _, _, err := c.Get(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("a zero-duration pin must not hold the entry; builder ran %d times", got)
	}
}

// TestGet_Singleflight pins that concurrent first requests share one build
// rather than stampeding the builder.
func TestGet_Singleflight(t *testing.T) {
	var calls atomic.Int64
	release := make(chan struct{})
	build := func(context.Context, []string) (*adk.Runner, []string, error) {
		calls.Add(1)
		<-release
		return &adk.Runner{}, nil, nil
	}
	c := New(build, time.Minute)

	const workers = 8
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			if _, _, err := c.Get(context.Background(), []string{"a"}); err != nil {
				t.Errorf("Get: %v", err)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("the builder ran %d times for %d concurrent requests, want 1", got, workers)
	}
}

func TestGet_BuildErrorIsNotCached(t *testing.T) {
	var calls atomic.Int64
	build := func(context.Context, []string) (*adk.Runner, []string, error) {
		if calls.Add(1) == 1 {
			return nil, nil, errors.New("boom")
		}
		return &adk.Runner{}, nil, nil
	}
	c := New(build, time.Minute)

	if _, _, err := c.Get(context.Background(), []string{"a"}); err == nil {
		t.Fatal("expected the first build to fail")
	}
	if c.Len() != 0 {
		t.Error("a failed build must not be cached")
	}
	runner, _, err := c.Get(context.Background(), []string{"a"})
	if err != nil || runner == nil {
		t.Fatalf("the retry must succeed, got (%v, %v)", runner, err)
	}
}
