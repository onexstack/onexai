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

package authn

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestMemoryNonceStoreClaimsEachNonceOnce pins the fallback store's contract:
// the first claim of a nonce succeeds, the second is a replay, and the key is
// scoped per caller so two services may use the same nonce value independently.
func TestMemoryNonceStoreClaimsEachNonceOnce(t *testing.T) {
	ctx := context.Background()
	s := newMemoryNonceStore()

	if fresh, err := s.Claim(ctx, "svc-chat", "n1"); err != nil || !fresh {
		t.Fatalf("first claim = (%v, %v), want (true, nil)", fresh, err)
	}
	if fresh, err := s.Claim(ctx, "svc-chat", "n1"); err != nil || fresh {
		t.Fatalf("replay claim = (%v, %v), want (false, nil)", fresh, err)
	}
	// Another caller reusing the same nonce value is not a replay for this one.
	if fresh, err := s.Claim(ctx, "svc-other", "n1"); err != nil || !fresh {
		t.Fatalf("other caller claim = (%v, %v), want (true, nil)", fresh, err)
	}
}

// TestMemoryNonceStoreForgetsAfterTheWindow pins the TTL: once the replay window
// has passed the timestamp check would reject the request anyway, so the store
// must not remember the nonce forever (that would leak memory per request).
func TestMemoryNonceStoreForgetsAfterTheWindow(t *testing.T) {
	ctx := context.Background()
	s := newMemoryNonceStore()
	now := time.Now()
	s.now = func() time.Time { return now }

	if fresh, _ := s.Claim(ctx, "svc-chat", "n1"); !fresh {
		t.Fatal("first claim was rejected")
	}

	now = now.Add(nonceTTL + time.Second)
	if fresh, err := s.Claim(ctx, "svc-chat", "n1"); err != nil || !fresh {
		t.Fatalf("claim after the window = (%v, %v), want (true, nil)", fresh, err)
	}
}

// TestRedisNonceStoreClaimsEachNonceOnce pins the deployment form against a real
// (in-process) Redis: SetNX is what makes the check atomic across pods, so the
// same nonce from two callers must yield exactly one success.
func TestRedisNonceStoreClaimsEachNonceOnce(t *testing.T) {
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	s := NewNonceStore(client, "onexai")

	if fresh, err := s.Claim(ctx, "svc-chat", "n1"); err != nil || !fresh {
		t.Fatalf("first claim = (%v, %v), want (true, nil)", fresh, err)
	}
	if fresh, err := s.Claim(ctx, "svc-chat", "n1"); err != nil || fresh {
		t.Fatalf("replay claim = (%v, %v), want (false, nil)", fresh, err)
	}

	// The key must carry the replay window as its TTL.
	if ttl := srv.TTL("onexai:agent:nonce:svc-chat:n1"); ttl != nonceTTL {
		t.Fatalf("nonce TTL = %s, want %s", ttl, nonceTTL)
	}
}
