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

package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// A run refused before bind borrowed the slot but nothing else. Unwinding here
// is what keeps that refusal from answering the conversation with
// ErrConversationBusy forever.
func TestRunLeaseUnwindBeforeBindReleasesTheSlot(t *testing.T) {
	b := New(Deps{MaxConcurrentPerConv: 1})

	lease, err := b.leaseRun("c1")
	if err != nil {
		t.Fatalf("leaseRun: %v", err)
	}
	if _, err := b.leaseRun("c1"); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("a second lease must be refused while the first is held, got %v", err)
	}

	lease.unwind()

	again, err := b.leaseRun("c1")
	if err != nil {
		t.Fatalf("the slot was not released: %v", err)
	}
	again.unwind()
}

// handoff moves ownership of the slot to the relay goroutine. A caller that
// also deferred unwind must not matter, and the slot must be free only once the
// relay has finished — the channel closes after the release defer runs.
func TestRunLeaseHandoffOwnsCleanup(t *testing.T) {
	b := New(Deps{MaxConcurrentPerConv: 1})

	lease, err := b.leaseRun("c1")
	if err != nil {
		t.Fatalf("leaseRun: %v", err)
	}
	if _, _ = lease.bind("run-1", 0, "user-1", "", "", nil); lease.ctx == nil {
		t.Fatal("bind must build the run context")
	}

	events := make(chan agentapi.Event)
	close(events)
	out := lease.handoff(events, suspend{RunID: "run-1"})

	// The caller's guard is a no-op after handoff.
	lease.unwind()

	for range out {
	}

	again, err := b.leaseRun("c1")
	if err != nil {
		t.Fatalf("the relay did not release the slot: %v", err)
	}
	again.unwind()
}

// stopReady is what turns the hard context cancel into the engine's safe-point
// stop. The replacement must be visible to Stop, which is the only reason the
// graceful stop ever runs.
func TestRunLeaseStopReadyReplacesTheRegistration(t *testing.T) {
	stops := runstop.NewLocal()
	b := New(Deps{MaxConcurrentPerConv: 1, Stops: stops})

	lease, err := b.leaseRun("c1")
	if err != nil {
		t.Fatalf("leaseRun: %v", err)
	}
	defer lease.unwind()

	if _, _ = lease.bind("run-1", 0, "user-1", "", "", nil); lease.ctx == nil {
		t.Fatal("bind must build the run context")
	}

	stopped := make(chan struct{}, 1)
	lease.stopReady("run-1")(func() { stopped <- struct{}{} })

	if err := b.Stop(context.Background(), "run-1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not reach the graceful stop registered by stopReady")
	}
}
