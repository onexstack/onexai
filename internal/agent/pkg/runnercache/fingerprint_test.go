// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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
	"sync/atomic"
	"testing"
	"time"
)

func TestFingerprintMismatchRebuilds(t *testing.T) {
	var calls atomic.Int64
	c := New(countingBuild(&calls), time.Minute)
	ctx := context.Background()

	first, _, _, err := c.GetWithTools(ctx, []string{"a"}, "fp1")
	if err != nil {
		t.Fatalf("GetWithTools: %v", err)
	}
	second, _, _, err := c.GetWithTools(ctx, []string{"a"}, "fp2")
	if err != nil {
		t.Fatalf("GetWithTools: %v", err)
	}
	if first == second {
		t.Error("a capability change must rebuild the graph")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("build called %d times, want 2", got)
	}

	// The new fingerprint is now the cached one.
	third, _, _, err := c.GetWithTools(ctx, []string{"a"}, "fp2")
	if err != nil {
		t.Fatalf("GetWithTools: %v", err)
	}
	if third != second {
		t.Error("the same fingerprint must reuse the cached graph")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("build called %d times, want 2", got)
	}
}

func TestPinnedEntrySurvivesFingerprintMismatch(t *testing.T) {
	var calls atomic.Int64
	c := New(countingBuild(&calls), time.Minute)
	ctx := context.Background()

	first, _, _, err := c.GetWithTools(ctx, []string{"a"}, "fp1")
	if err != nil {
		t.Fatalf("GetWithTools: %v", err)
	}
	c.Pin([]string{"a"}, time.Minute)

	// A run is paused against the first graph, so the capability change must not
	// replace it until the pause ends: the checkpoint addresses the graph.
	pinned, _, _, err := c.GetWithTools(ctx, []string{"a"}, "fp2")
	if err != nil {
		t.Fatalf("GetWithTools: %v", err)
	}
	if pinned != first {
		t.Error("a pinned graph must be served across a fingerprint change")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("build called %d times, want 1", got)
	}

	c.Unpin([]string{"a"})
	rebuilt, _, _, err := c.GetWithTools(ctx, []string{"a"}, "fp2")
	if err != nil {
		t.Fatalf("GetWithTools: %v", err)
	}
	if rebuilt == first {
		t.Error("after the pause ends the graph must be rebuilt")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("build called %d times, want 2", got)
	}
}

func TestKeyUnchangedByFingerprint(t *testing.T) {
	var calls atomic.Int64
	c := New(countingBuild(&calls), time.Minute)
	ctx := context.Background()

	_, _, key1, err := c.GetWithTools(ctx, []string{"b", "a"}, "fp1")
	if err != nil {
		t.Fatalf("GetWithTools: %v", err)
	}
	_, _, key2, err := c.GetWithTools(ctx, []string{"a", "b"}, "fp2")
	if err != nil {
		t.Fatalf("GetWithTools: %v", err)
	}
	if key1 != key2 || key1 != "a.b" {
		t.Errorf("keys must depend on the agent set alone: %q vs %q", key1, key2)
	}
}
