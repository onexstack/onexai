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

package idempotent

import (
	"context"
	"testing"
	"time"
)

func newTestStore(ttl time.Duration) *memoryStore {
	s := newMemoryStore(ttl)
	s.now = func() time.Time { return time.Unix(0, 0) }

	return s
}

func TestTheFirstClaimWins(t *testing.T) {
	store := New(nil, "")
	ctx := context.Background()

	fresh, err := store.Claim(ctx, "k")
	if err != nil || !fresh {
		t.Fatalf("first claim = (%v, %v), want (true, nil)", fresh, err)
	}

	fresh, err = store.Claim(ctx, "k")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if fresh {
		t.Error("the second claim of one key must not be fresh")
	}
}

// A request that failed to do anything must be retryable at once, rather than
// being refused until the TTL runs out.
func TestReleaseAllowsTheSameKeyAgain(t *testing.T) {
	store := New(nil, "")
	ctx := context.Background()

	if _, err := store.Claim(ctx, "k"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if err := store.Release(ctx, "k"); err != nil {
		t.Fatalf("release: %v", err)
	}

	fresh, err := store.Claim(ctx, "k")
	if err != nil || !fresh {
		t.Errorf("claim after release = (%v, %v), want (true, nil)", fresh, err)
	}
}

func TestAKeyIsForgottenAfterItsTTL(t *testing.T) {
	store := newTestStore(time.Hour)
	ctx := context.Background()

	if _, err := store.Claim(ctx, "k"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	store.now = func() time.Time { return time.Unix(0, 0).Add(2 * time.Hour) }

	fresh, err := store.Claim(ctx, "k")
	if err != nil || !fresh {
		t.Errorf("claim after the TTL = (%v, %v), want (true, nil)", fresh, err)
	}
}

// Keys are independent: claiming one must not refuse another.
func TestKeysAreIndependent(t *testing.T) {
	store := New(nil, "")
	ctx := context.Background()

	if _, err := store.Claim(ctx, "a"); err != nil {
		t.Fatalf("claim a: %v", err)
	}

	fresh, err := store.Claim(ctx, "b")
	if err != nil || !fresh {
		t.Errorf("claim b = (%v, %v), want (true, nil)", fresh, err)
	}
}
