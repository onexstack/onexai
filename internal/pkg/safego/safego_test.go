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

package safego

import (
	"context"
	"sync"
	"testing"
	"time"
)

// probe registers a reporter and returns the channel it feeds.
//
// Every test that panics must wait for its report rather than for its
// goroutine: the panicking function's own defers run before Recover does, so a
// WaitGroup signalled from inside it is released strictly before the panic has
// been reported — and the report then lands in whichever test runs next.
func probe(t *testing.T) <-chan string {
	t.Helper()
	reports := make(chan string, 8)
	OnPanic(func(_ context.Context, where string) { reports <- where })
	t.Cleanup(func() { panicReporter.Store(nil) })
	return reports
}

// waitFor reports until every name in want has been seen. It accumulates
// rather than looking for one at a time: a report that does not match the name
// being waited for is still a report, and discarding it would lose it.
func waitFor(t *testing.T, reports <-chan string, want ...string) {
	t.Helper()
	remaining := make(map[string]bool, len(want))
	for _, w := range want {
		remaining[w] = true
	}
	deadline := time.After(2 * time.Second)
	for len(remaining) > 0 {
		select {
		case got := <-reports:
			delete(remaining, got)
		case <-deadline:
			t.Fatalf("never reported: %v", remaining)
		}
	}
}

// TestGo_SurvivesAPanic pins the whole point: a panic in a request's own
// goroutine must not take the process with it.
func TestGo_SurvivesAPanic(t *testing.T) {
	reports := probe(t)
	Go(context.Background(), "survives", func() { panic("boom") })
	waitFor(t, reports, "survives") // reaching here at all is the assertion
}

func TestGo_RunsTheFunction(t *testing.T) {
	done := make(chan struct{})
	Go(context.Background(), "runs", func() { close(done) })

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the function did not run")
	}
}

// TestOnPanic_IsRolledBackByCleanup is not about the hook; it asserts the test
// scaffolding above, so a failure there is read as a test problem rather than
// as a product one.
func TestOnPanic_IsRolledBackByCleanup(t *testing.T) {
	reports := probe(t)
	Go(context.Background(), "rollback", func() { panic("boom") })
	waitFor(t, reports, "rollback")
}

func TestOnPanic_IsCalledOncePerPanic(t *testing.T) {
	reports := probe(t)

	want := []string{"agent run", "agent relay"}
	for _, where := range want {
		Go(context.Background(), where, func() { panic("boom") })
	}
	waitFor(t, reports, want...)
}

func TestOnPanic_IsNotCalledWithoutAPanic(t *testing.T) {
	reports := probe(t)

	done := make(chan struct{})
	Go(context.Background(), "quiet", func() { close(done) })
	<-done

	select {
	case where := <-reports:
		t.Errorf("the reporter fired for a goroutine that did not panic: %q", where)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRecover_IsANoOpWhenNothingPanicked(t *testing.T) {
	func() {
		defer Recover(context.Background(), "test")
	}()
}

// TestRecover_InPlaceIsDeterministic pins the ordering the tests above depend
// on: used directly as a defer, Recover runs before any defer registered after
// it, so a caller can wait on its own work being reported.
func TestRecover_InPlaceIsDeterministic(t *testing.T) {
	reports := probe(t)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done() // registered first, so it runs last
		defer Recover(context.Background(), "in place")
		panic("boom")
	}()

	waitFor(t, reports, "in place")
	wg.Wait()
}
