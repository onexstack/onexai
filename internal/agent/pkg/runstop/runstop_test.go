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

package runstop

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLocalRegistryStopsTheRunItHolds(t *testing.T) {
	reg := NewLocal()
	var stopped atomic.Int32
	deregister := reg.Register("run-1", func() { stopped.Add(1) })

	if !reg.StopLocal("run-1") {
		t.Fatal("a registered run was not found")
	}
	if got := stopped.Load(); got != 1 {
		t.Fatalf("the cancel ran %d times, want 1", got)
	}

	// Deregistering is what a finished run does; a stop afterwards must not run
	// a cancel that belongs to a turn already over.
	deregister()
	if reg.StopLocal("run-1") {
		t.Fatal("a deregistered run was still found")
	}
	if got := stopped.Load(); got != 1 {
		t.Fatalf("a step after deregistration ran the cancel again: %d", got)
	}
}

// TestRedisStopReachesTheHoldingPod pins the cross-pod contract: a stop clicked
// on the pod that does not hold the run still cancels it on the pod that does,
// and leaves every other run alone.
func TestRedisStopReachesTheHoldingPod(t *testing.T) {
	srv := miniredis.RunT(t)
	newClient := func() *redis.Client {
		c := redis.NewClient(&redis.Options{Addr: srv.Addr()})
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	// Two pods: A holds the run being stopped, B is where the click happens.
	holder := New(newClient(), "onex")
	clicker := New(newClient(), "onex")

	for _, reg := range []Registry{holder, clicker} {
		sub, ok := reg.(Subscriber)
		if !ok {
			t.Fatal("the Redis registry does not subscribe, so a cross-pod stop could never land")
		}
		// The test's own context ends the subscription when the test does.
		go sub.Subscribe(t.Context())
	}
	// Pub/Sub has no replay: a message published before both subscribers are
	// registered is simply lost, and the test would then fail for a timing
	// reason rather than a behaviour one.
	waitFor(t, func() bool {
		return srv.PubSubNumSub("onex:askai:stop")["onex:askai:stop"] == 2
	})

	var heldStopped, bystanderStopped atomic.Int32
	holder.Register("run-held", func() { heldStopped.Add(1) })
	clicker.Register("run-bystander", func() { bystanderStopped.Add(1) })

	if err := clicker.RequestStop(context.Background(), "run-held"); err != nil {
		t.Fatalf("RequestStop: %v", err)
	}

	waitFor(t, func() bool { return heldStopped.Load() == 1 })
	// The local side of the clicker had nothing to stop; the broadcast is not a
	// licence to cancel whatever this pod happens to be running.
	if got := bystanderStopped.Load(); got != 0 {
		t.Fatalf("an unrelated run was stopped by another run's signal: %d", got)
	}
}

// waitFor polls until cond holds, failing the test after a short bound. Pub/Sub
// delivery is asynchronous by nature, so the alternative is a fixed sleep.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the cross-pod stop to arrive")
}
