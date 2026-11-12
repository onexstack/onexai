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

package mirror

import (
	"testing"
	"time"
)

func TestIndexReportsOnlyUnseenEntriesAsStale(t *testing.T) {
	index := newIndex()
	index.seed([]CachedEntry{
		{Namespace: "ns", Name: "kept", SpecHash: "s1", StatusHash: "t1"},
		{Namespace: "ns", Name: "gone", SpecHash: "s2", StatusHash: "t2"},
	})

	passStart := time.Now()
	index.touch("ns", "kept", passStart)

	stale := index.stale(passStart)
	if len(stale) != 1 || stale[0].Name != "gone" || stale[0].Namespace != "ns" {
		t.Fatalf("stale = %+v, want exactly ns/gone", stale)
	}
}

func TestIndexKeepsRowsWrittenDuringAPass(t *testing.T) {
	index := newIndex()
	passStart := time.Now()

	// A row written by a reconcile while the pass runs carries a later seenAt,
	// which is what stops the sweep from deleting an object created mid-pass.
	index.put("ns", "fresh", "s", "t", passStart.Add(time.Millisecond))

	if stale := index.stale(passStart); len(stale) != 0 {
		t.Fatalf("a row written during the pass was called stale: %+v", stale)
	}
}

func TestIndexForgetRemovesTheEntry(t *testing.T) {
	index := newIndex()
	index.put("ns", "obj", "s", "t", time.Now())
	index.forget("ns", "obj")

	if _, ok := index.get("ns", "obj"); ok {
		t.Fatal("forget left the entry behind")
	}
	if index.len() != 0 {
		t.Fatalf("len = %d after forget", index.len())
	}
}

func TestSplitIndexKeyKeepsNestedNames(t *testing.T) {
	namespace, name := splitIndexKey("default/sub/name")
	if namespace != "default" || name != "sub/name" {
		t.Fatalf("split = %q/%q", namespace, name)
	}
}
