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

package handler

import (
	"context"
	"testing"

	"github.com/dgraph-io/ristretto/v2"
)

// newDedupHandler builds the part of a handler the dedup path touches. The
// production constructor also wires the hub and the biz, which this test neither
// needs nor could build.
func newDedupHandler(t *testing.T) *Handler {
	t.Helper()
	local, err := ristretto.NewCache(&ristretto.Config[string, struct{}]{
		NumCounters: 2000,
		MaxCost:     200,
		BufferItems: 1000,
	})
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	return &Handler{local: local}
}

// TestDuplicateDropped pins what makes a retried Lark delivery harmless: the
// second delivery of an event id is recognised and dropped rather than turned
// into a second turn.
//
// The Wait calls are the test's, not the code's. Ristretto admits writes through
// a buffer, so a read immediately after a write can miss it — which is fine where
// this is used (Lark retries arrive seconds apart) and not fine in a test that
// would then be asserting on a race. Wait flushes the buffer so the assertion is
// about isDuplicate rather than about the cache's timing; see the note at the end
// of this file for what that leaves unverified.
func TestDuplicateDropped(t *testing.T) {
	h := newDedupHandler(t)
	ctx := context.Background()

	// An event with no id is never a duplicate. Remembering "" would make the
	// first id-less event swallow every one after it.
	for i := range 3 {
		if h.isDuplicate(ctx, "") {
			t.Fatalf("call %d: an event with no id was treated as a duplicate", i)
		}
	}

	if h.isDuplicate(ctx, "evt-1") {
		t.Fatal("the first delivery of an event was treated as a duplicate")
	}
	h.local.Wait()

	if !h.isDuplicate(ctx, "evt-1") {
		t.Error("the second delivery of an event was processed again")
	}
	// Recognising an event does not consume it: a third retry is still a retry.
	if !h.isDuplicate(ctx, "evt-1") {
		t.Error("an event was a duplicate once and not the second time")
	}

	// Ids are independent. One event's retry must not silence another event.
	if h.isDuplicate(ctx, "evt-2") {
		t.Error("a different event id was treated as a duplicate of evt-1")
	}
}

// A note on what this file does not cover: the mutex around the check-then-act
// serialises the calls, but ristretto admits writes asynchronously, so two
// deliveries of the same id that arrive within the buffer's processing window can
// both be seen as new. For Lark retries — seconds apart — that window is not
// reachable, and a synchronous admission (a Wait after the write) would put the
// cache's cost on every inbound event instead. It is recorded here rather than
// asserted, because a test cannot state a guarantee the code does not make.
