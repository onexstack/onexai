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

package checkpoint

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// The contract below is exercised against the in-memory store, which is the one
// a deployment without Redis answers approvals through. The Redis store's
// remaining paths — the TTL, and SetNX deciding a claim — need a server; what
// can be pinned without one is pinned here, and the two stores are kept in step
// by sharing this one contract rather than by testing them apart.

// deadRedis points at a port nothing listens on. Connecting is refused at once
// rather than timing out, so a test that only needs a backend to fail gets that
// failure in microseconds. It is how the Redis store's error paths are covered
// without a server: what matters about them is that a broken backend is never
// mistaken for an absent suspension, and that is a property of the code, not of
// the server.
func deadRedis(t *testing.T) *redis.Client {
	t.Helper()
	return redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Second})
}

func TestMemStoreContract(t *testing.T) {
	ctx := t.Context()

	t.Run("an unknown run has no checkpoint", func(t *testing.T) {
		s := newMemStore()
		data, ok, err := s.Get(ctx, "run-1")
		if err != nil {
			t.Fatalf("Get() error = %v, want nil", err)
		}
		if ok || data != nil {
			t.Errorf("Get() = (%q, %v), want (nil, false)", data, ok)
		}
	})

	t.Run("a checkpoint reads back what was written", func(t *testing.T) {
		s := newMemStore()
		if err := s.Set(ctx, "run-1", []byte("blob")); err != nil {
			t.Fatalf("Set() error = %v, want nil", err)
		}
		data, ok, err := s.Get(ctx, "run-1")
		if err != nil || !ok {
			t.Fatalf("Get() = (_, %v, %v), want the checkpoint to be found", ok, err)
		}
		if string(data) != "blob" {
			t.Errorf("Get() = %q, want %q", data, "blob")
		}
	})

	t.Run("writing again replaces the checkpoint", func(t *testing.T) {
		// A run pauses more than once, and each pause writes its own checkpoint
		// under the run's id. A store that kept the first would resume a run into
		// a state it has already left.
		s := newMemStore()
		if err := s.Set(ctx, "run-1", []byte("first")); err != nil {
			t.Fatalf("Set() error = %v, want nil", err)
		}
		if err := s.Set(ctx, "run-1", []byte("second")); err != nil {
			t.Fatalf("Set() error = %v, want nil", err)
		}
		data, _, _ := s.Get(ctx, "run-1")
		if string(data) != "second" {
			t.Errorf("Get() = %q, want the later checkpoint %q", data, "second")
		}
	})

	t.Run("runs do not read each other's checkpoints", func(t *testing.T) {
		s := newMemStore()
		if err := s.Set(ctx, "run-1", []byte("one")); err != nil {
			t.Fatalf("Set() error = %v, want nil", err)
		}
		if err := s.Set(ctx, "run-2", []byte("two")); err != nil {
			t.Fatalf("Set() error = %v, want nil", err)
		}
		data, _, _ := s.Get(ctx, "run-2")
		if string(data) != "two" {
			t.Errorf("Get(run-2) = %q, want %q", data, "two")
		}
	})

	t.Run("a run's metadata reads back with it", func(t *testing.T) {
		s := newMemStore()
		want := fullMeta()
		if err := s.PutMeta(ctx, "run-1", want); err != nil {
			t.Fatalf("PutMeta() error = %v, want nil", err)
		}
		got, ok, err := s.GetMeta(ctx, "run-1")
		if err != nil || !ok {
			t.Fatalf("GetMeta() = (_, %v, %v), want the metadata to be found", ok, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("GetMeta() = %+v, want %+v", got, want)
		}
	})

	t.Run("an unknown run has no metadata", func(t *testing.T) {
		s := newMemStore()
		meta, ok, err := s.GetMeta(ctx, "run-1")
		if err != nil {
			t.Fatalf("GetMeta() error = %v, want nil", err)
		}
		if ok || !reflect.DeepEqual(meta, agent.RunMeta{}) {
			t.Errorf("GetMeta() = (%+v, %v), want the zero RunMeta and false", meta, ok)
		}
	})

	t.Run("deleting a run removes its checkpoint and its metadata", func(t *testing.T) {
		// The two are one unit: a resume rebuilds the run from the metadata and
		// then executes against the checkpoint. A surviving checkpoint whose
		// metadata is gone is the half-state that Delete exists to prevent.
		s := newMemStore()
		if err := s.Set(ctx, "run-1", []byte("blob")); err != nil {
			t.Fatalf("Set() error = %v, want nil", err)
		}
		if err := s.PutMeta(ctx, "run-1", fullMeta()); err != nil {
			t.Fatalf("PutMeta() error = %v, want nil", err)
		}
		if err := s.Delete(ctx, "run-1"); err != nil {
			t.Fatalf("Delete() error = %v, want nil", err)
		}

		if _, ok, _ := s.Get(ctx, "run-1"); ok {
			t.Error("the checkpoint survived Delete")
		}
		if _, ok, _ := s.GetMeta(ctx, "run-1"); ok {
			t.Error("the metadata survived Delete")
		}
	})

	t.Run("deleting twice is not an error", func(t *testing.T) {
		// The second click on an approval card is expected, not exceptional. It
		// is the caller that has to say the request was already handled; the
		// store must not turn it into a failure the caller has to decode.
		s := newMemStore()
		if err := s.Set(ctx, "run-1", []byte("blob")); err != nil {
			t.Fatalf("Set() error = %v, want nil", err)
		}
		if err := s.Delete(ctx, "run-1"); err != nil {
			t.Fatalf("first Delete() error = %v, want nil", err)
		}
		if err := s.Delete(ctx, "run-1"); err != nil {
			t.Errorf("second Delete() error = %v, want nil", err)
		}
	})

	t.Run("deleting a run leaves its neighbours alone", func(t *testing.T) {
		s := newMemStore()
		for _, id := range []string{"run-1", "run-2"} {
			if err := s.Set(ctx, id, []byte(id)); err != nil {
				t.Fatalf("Set(%s) error = %v, want nil", id, err)
			}
		}
		if err := s.Delete(ctx, "run-1"); err != nil {
			t.Fatalf("Delete() error = %v, want nil", err)
		}
		if data, ok, _ := s.Get(ctx, "run-2"); !ok || string(data) != "run-2" {
			t.Errorf("Get(run-2) = (%q, %v), want the untouched checkpoint", data, ok)
		}
	})

	t.Run("the first answer to a suspension wins", func(t *testing.T) {
		s := newMemStore()
		first, err := s.Claim(ctx, "run-1", "req-1")
		if err != nil || !first {
			t.Fatalf("first Claim() = (%v, %v), want (true, nil)", first, err)
		}
		second, err := s.Claim(ctx, "run-1", "req-1")
		if err != nil {
			t.Fatalf("second Claim() error = %v, want nil", err)
		}
		if second {
			t.Error("the second answer claimed a suspension the first had already answered")
		}
	})

	t.Run("a later suspension of the same run is answerable", func(t *testing.T) {
		// One run can pause several times. Keying the claim by run alone would
		// make every pause after the first unanswerable.
		s := newMemStore()
		if _, err := s.Claim(ctx, "run-1", "req-1"); err != nil {
			t.Fatalf("Claim(req-1) error = %v, want nil", err)
		}
		ok, err := s.Claim(ctx, "run-1", "req-2")
		if err != nil {
			t.Fatalf("Claim(req-2) error = %v, want nil", err)
		}
		if !ok {
			t.Error("a later suspension of the same run could not be claimed")
		}
	})

	t.Run("one run's answer does not claim another's", func(t *testing.T) {
		s := newMemStore()
		if _, err := s.Claim(ctx, "run-1", "req-1"); err != nil {
			t.Fatalf("Claim() error = %v, want nil", err)
		}
		ok, err := s.Claim(ctx, "run-2", "req-1")
		if err != nil {
			t.Fatalf("Claim() error = %v, want nil", err)
		}
		if !ok {
			t.Error("an answer meant for another run claimed this one's suspension")
		}
	})

	t.Run("an answer with no request id claims nothing", func(t *testing.T) {
		// A caller that names no suspension is answered by nobody: the request id
		// is what a resume matches against, so an empty one is a malformed answer
		// rather than a valid first answer.
		s := newMemStore()
		ok, err := s.Claim(ctx, "run-1", "")
		if err != nil {
			t.Fatalf("Claim() error = %v, want nil", err)
		}
		if ok {
			t.Error("an empty request id claimed a suspension")
		}
	})

	t.Run("claiming is decided one caller at a time", func(t *testing.T) {
		// First-one-wins has to hold under concurrency, which is the whole point
		// of the claim: two card clicks arrive as two requests, on two goroutines,
		// and a gate around a destructive tool is worth nothing if both resume.
		s := newMemStore()
		const callers = 8

		start := make(chan struct{})
		results := make(chan bool, callers)
		for range callers {
			go func() {
				<-start
				ok, err := s.Claim(ctx, "run-1", "req-1")
				if err != nil {
					t.Errorf("Claim() error = %v, want nil", err)
				}
				results <- ok
			}()
		}
		close(start)

		won := 0
		for range callers {
			if <-results {
				won++
			}
		}
		if won != 1 {
			t.Errorf("%d callers claimed the same suspension, want exactly 1", won)
		}
	})
}

func TestNewChoosesStoreByBackend(t *testing.T) {
	// A deployment with no Redis is the single-process one, whose suspensions
	// live in memory. The choice is made here rather than at each call site so
	// that the rest of the code never has to know which backend it has.
	if _, ok := New(nil, "onex", time.Hour).(*memStore); !ok {
		t.Error("New(nil, ...) is not the in-memory store, want the no-Redis fallback")
	}
	rdb := deadRedis(t)
	defer rdb.Close()
	s, ok := New(rdb, "onex", time.Hour).(*redisStore)
	if !ok {
		t.Fatal("New(rdb, ...) is not the Redis store")
	}
	if s.prefix != "onex" || s.ttl != time.Hour {
		t.Errorf("New() built a store with prefix %q and ttl %v, want %q and %v", s.prefix, s.ttl, "onex", time.Hour)
	}
}

func TestRedisStoreKeysAreDistinct(t *testing.T) {
	// Checkpoint, metadata and claim live under one run id. Reading a run's
	// metadata back as its checkpoint — or worse, a claim as a checkpoint, which
	// would resume a run against the string "1" — is prevented by nothing but
	// the keys being different, so they are pinned here.
	s := &redisStore{prefix: "onex"}

	keys := map[string]string{
		"checkpoint": s.checkpointKey("run-1"),
		"metadata":   s.metaKey("run-1"),
		"claim":      s.claimKey("run-1", "req-1"),
	}
	seen := map[string]string{}
	for name, key := range keys {
		if prev, dup := seen[key]; dup {
			t.Errorf("the %s key equals the %s key: %q", name, prev, key)
		}
		seen[key] = name
	}

	// Every key is scoped to the configured prefix: two deployments sharing a
	// Redis database must not read each other's suspensions.
	for name, key := range keys {
		if len(key) < len("onex:") || key[:len("onex:")] != "onex:" {
			t.Errorf("the %s key %q is not under the configured prefix", name, key)
		}
	}

	// A claim is per suspension, so a second answer to the same run is not
	// blocked by the first having been given.
	if s.claimKey("run-1", "req-1") == s.claimKey("run-1", "req-2") {
		t.Error("two suspensions of one run share a claim key")
	}
}

func TestRedisStoreDoesNotMistakeFailureForAbsence(t *testing.T) {
	// Get's two-value-plus-error signature is what lets a resume tell "no such
	// suspension" from "the store is unreachable", and the two demand opposite
	// responses: report the request as already handled, or report a failure. A
	// store that swallowed its error into a false would answer a click with
	// "已处理或已失效" (already handled or expired) while the run was still parked,
	// which reads as the answer having been accepted.
	s := &redisStore{rdb: deadRedis(t), prefix: "onex", ttl: time.Minute}
	defer s.rdb.Close()
	ctx := t.Context()

	if _, ok, err := s.Get(ctx, "run-1"); err == nil || ok {
		t.Errorf("Get() = (_, %v, %v), want an error and no checkpoint", ok, err)
	}
	if _, ok, err := s.GetMeta(ctx, "run-1"); err == nil || ok {
		t.Errorf("GetMeta() = (_, %v, %v), want an error and no metadata", ok, err)
	}
	if err := s.Set(ctx, "run-1", []byte("blob")); err == nil {
		t.Error("Set() reported success against an unreachable store")
	}
	if err := s.Delete(ctx, "run-1"); err == nil {
		t.Error("Delete() reported success against an unreachable store")
	}
	if err := s.PutMeta(ctx, "run-1", fullMeta()); err == nil {
		t.Error("PutMeta() reported success against an unreachable store")
	}
}

func TestRedisStoreClaimsNothingWithoutARequestID(t *testing.T) {
	// The refusal happens before Redis is touched, so it holds even when the
	// store is unreachable: a malformed answer is refused on its own terms
	// rather than reported as a backend failure.
	s := &redisStore{rdb: deadRedis(t), prefix: "onex", ttl: time.Minute}
	defer s.rdb.Close()

	ok, err := s.Claim(t.Context(), "run-1", "")
	if err != nil {
		t.Fatalf("Claim() error = %v, want nil", err)
	}
	if ok {
		t.Error("an empty request id claimed a suspension")
	}
}

func TestRunMetaSurvivesTheWireEncoding(t *testing.T) {
	// The in-memory store hands a RunMeta back as the struct it was given, so it
	// cannot catch a field that json would drop — and Redis is the only place
	// one is serialized. A lost tag is worth pinning here rather than discovering
	// it as a resume that cannot rebuild its run, since what the field carries
	// (the agent selection, the asker, the suspension awaiting an answer) has no
	// other copy.
	want := fullMeta()

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v, want nil", err)
	}
	var got agent.RunMeta
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a RunMeta round-trip gave %+v, want %+v", got, want)
	}

	// Decoding is what a resume does, and a resume must not need a field that an
	// older writer never sent: the metadata outlives the process that wrote it.
	var older agent.RunMeta
	if err := json.Unmarshal([]byte(`{"agents":["claude-code"]}`), &older); err != nil {
		t.Fatalf("Unmarshal() of an older blob error = %v, want nil", err)
	}
	if !reflect.DeepEqual(older.Agents, []string{"claude-code"}) || older.RequestID != "" {
		t.Errorf("Unmarshal() = %+v, want the agents and nothing else", older)
	}
}

// fullMeta is a RunMeta with every field set, so a field that does not survive
// the round trip shows up as a difference rather than as a zero value matching a
// zero value.
func fullMeta() agent.RunMeta {
	return agent.RunMeta{
		Agents:      []string{"claude-code", "onexai"},
		SessionID:   "oc_1",
		UserMessage: agent.Message{Role: "user", Content: "删掉测试集群", Images: []media.Ref{{URL: "data:image/png;base64,AA"}}},
		UserID:      "ou_asker",
		Reasoning:   true,
		AllowAll:    true,
		RequestID:   "req-1",
		Composition: &agent.AgentComposition{
			Prompts: []string{"ops-persona"},
			Skills:  []string{"sre"},
			MCPs:    []string{"incident"},
			Model:   "fast",
		},
		Pending: []agent.PendingAddress{
			{Address: "tool_1", Kind: agent.InterruptKindPermission, ToolName: "delete_cluster"},
			{Address: "tool_2", Kind: agent.InterruptKindQuestion, ToolName: "ask_user_question"},
		},
	}
}
