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
	"sync"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/agent/pkg/usage"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// pendingEvents collects everything currently buffered on out. Unlike drain it
// does not wait for the channel to close, so it can inspect a run mid-flight.
func pendingEvents(out <-chan agentapi.Event) []agentapi.Event {
	var got []agentapi.Event
	for {
		select {
		case ev := <-out:
			got = append(got, ev)
		default:
			return got
		}
	}
}

// TestRunEmitterDropsSendsAfterFinish covers the failure this type exists for: a
// tool running on the ADK's goroutine reports its result while the engine's loop
// is already finishing the run and closing the channel. Without the flag that
// send would panic the whole agent process.
func TestRunEmitterDropsSendsAfterFinish(t *testing.T) {
	out := make(chan agentapi.Event, 64)
	em := newRunEmitter(out)

	// A reader in its own goroutine, standing in for agentstream.Pump, which
	// always drains and is what makes holding the lock across a send safe.
	var mu sync.Mutex
	var delivered []agentapi.Event
	read := make(chan struct{})
	go func() {
		defer close(read)
		for ev := range out {
			mu.Lock()
			delivered = append(delivered, ev)
			mu.Unlock()
		}
	}()

	// The tool goroutine keeps reporting for as long as the engine is finishing.
	// Every one of these sends loses the race.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			em.send(agentapi.Event{Type: agentapi.EventToolStep, Steps: []agentapi.ToolStep{{
				ID: "late", Name: "slow-tool", Status: "ok",
			}}})
		}
	}()

	em.finish(agentapi.Event{Type: agentapi.EventDone, Final: "answer"})
	close(out) // what the engine does next, via its deferred close
	close(stop)
	wg.Wait()
	<-read

	// The terminal event is the last thing delivered: nothing raced past it, and
	// nothing panicked the process to get there.
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) == 0 {
		t.Fatal("no event was delivered at all")
	}
	for i, ev := range delivered {
		if ev.Type == agentapi.EventDone && i != len(delivered)-1 {
			t.Fatalf("the done event was followed by %d more: %+v", len(delivered)-i-1, delivered[i+1:])
		}
	}
	if last := delivered[len(delivered)-1]; last.Type != agentapi.EventDone {
		t.Fatalf("last delivered event = %+v, want done", last)
	}
}

// TestRunEmitterFinishIsOnce pins that a second terminal event cannot be
// delivered — the run has exactly one outcome.
func TestRunEmitterFinishIsOnce(t *testing.T) {
	out := make(chan agentapi.Event, 8)
	em := newRunEmitter(out)

	em.finish(agentapi.Event{Type: agentapi.EventDone, Final: "answer"})
	em.finish(agentapi.Event{Type: agentapi.EventFailed})

	if got := pendingEvents(out); len(got) != 1 || got[0].Type != agentapi.EventDone {
		t.Fatalf("got %+v, want the single done event", got)
	}
}

// TestUsageIfChangedBackfillsInlineTokens is the core of the inline token
// figure: a tool batch is reported without tokens, and once the round that
// follows reports its usage the same steps are re-sent — same ID, same status,
// same duration — carrying the context growth that batch caused. The ID is what
// lets a renderer update the row in place instead of adding a second one.
func TestUsageIfChangedBackfillsInlineTokens(t *testing.T) {
	out := make(chan agentapi.Event, 64)
	em := newRunEmitter(out)
	tracker := usage.New()

	// Round 1: the model answers with a tool call. Its usage is recorded before
	// the call is handed on, and its prompt is the standalone user question.
	tracker.Record(usage.Call{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
	em.usageIfChanged(tracker)
	if got := pendingEvents(out); len(got) != 1 || got[0].Type != agentapi.EventUsage {
		t.Fatalf("round 1 reported %+v, want just a usage event", got)
	}

	// The tools that round asked for run, each reporting a pending step and then
	// a terminal one.
	em.send(agentapi.Event{Type: agentapi.EventToolStep, Steps: []agentapi.ToolStep{
		{ID: "call_a", Name: "search", Status: "pending"},
	}})
	em.send(agentapi.Event{Type: agentapi.EventToolStep, Steps: []agentapi.ToolStep{
		{ID: "call_a", Name: "search", Status: "ok", Duration: 234 * time.Millisecond},
	}})

	// No new call has been recorded, so nothing is attributable yet and nothing
	// more goes out.
	em.usageIfChanged(tracker)
	if got := pendingEvents(out); len(got) != 2 {
		t.Fatalf("got %d events before any new call, want the 2 tool steps", len(got))
	} else if got[0].Steps[0].Status != "pending" || got[1].Steps[0].Status != "ok" {
		t.Fatalf("tool steps were altered: %+v", got)
	}

	// Round 2: the prompt now carries round 1's prompt, its completion and the
	// tool result — so the result's own share is what is left over.
	tracker.Record(usage.Call{PromptTokens: 400, CompletionTokens: 30, TotalTokens: 430})

	em.usageIfChanged(tracker)

	got := pendingEvents(out)
	if len(got) != 2 {
		t.Fatalf("got %d events, want a usage event and one backfilled step", len(got))
	}
	if got[0].Type != agentapi.EventUsage {
		t.Fatalf("first event is %v, want usage", got[0].Type)
	}
	if u := got[0].Usage; u == nil || u.Calls != 2 || u.TotalTokens != 550 || u.PromptTokens != 500 {
		t.Fatalf("usage event = %+v, want 2 calls totalling 550 over 500 prompt tokens", u)
	}

	step := got[1]
	if step.Type != agentapi.EventToolStep || len(step.Steps) != 1 {
		t.Fatalf("second event = %+v, want a single backfilled step", step)
	}
	// 400 − 100 − 20 = 280.
	if st := step.Steps[0]; st.ID != "call_a" || st.Status != "ok" ||
		st.Duration != 234*time.Millisecond || st.Tokens != 280 {
		t.Fatalf("backfilled step = %+v, want call_a/ok/234ms/280", st)
	}

	// The batch has been attributed, so a third call with no new tools reports
	// usage alone rather than repeating the step.
	tracker.Record(usage.Call{PromptTokens: 500, CompletionTokens: 10, TotalTokens: 510})
	em.usageIfChanged(tracker)
	if got := pendingEvents(out); len(got) != 1 || got[0].Type != agentapi.EventUsage {
		t.Fatalf("got %+v, want a lone usage event", got)
	}
}

// TestUsageIfChangedParallelBatchSharesDelta documents the limit of a figure
// derived from per-call usage: tools called in one batch are answered by a
// single following call, so they cannot be told apart and share its delta.
func TestUsageIfChangedParallelBatchSharesDelta(t *testing.T) {
	out := make(chan agentapi.Event, 64)
	em := newRunEmitter(out)
	tracker := usage.New()

	tracker.Record(usage.Call{PromptTokens: 100, CompletionTokens: 20})
	em.usageIfChanged(tracker)
	for _, id := range []string{"call_a", "call_b"} {
		em.send(agentapi.Event{Type: agentapi.EventToolStep, Steps: []agentapi.ToolStep{{
			ID: id, Name: id, Status: "ok", Duration: time.Second,
		}}})
	}

	// Both tools ran in parallel, so the following call is answered by one
	// prompt delta covering both results at once.
	tracker.Record(usage.Call{PromptTokens: 320, CompletionTokens: 30})
	pendingEvents(out) // discard round 1's usage event
	em.usageIfChanged(tracker)

	var tokens []int
	for _, ev := range pendingEvents(out) {
		if ev.Type == agentapi.EventToolStep {
			tokens = append(tokens, ev.Steps[0].Tokens)
		}
	}
	// 320 − 100 − 20 = 200, reported identically for both.
	if len(tokens) != 2 || tokens[0] != 200 || tokens[1] != 200 {
		t.Fatalf("batch tokens = %v, want two rows of 200", tokens)
	}
}

// TestUsageIfChangedClampsNonGrowth keeps a derived figure honest: a prompt that
// did not grow (or shrank, e.g. via a context window trim) yields no token
// annotation rather than a negative or invented one.
func TestUsageIfChangedClampsNonGrowth(t *testing.T) {
	for _, prompt := range []int{100, 90} {
		out := make(chan agentapi.Event, 64)
		em := newRunEmitter(out)
		tracker := usage.New()

		tracker.Record(usage.Call{PromptTokens: 100, CompletionTokens: 20})
		em.send(agentapi.Event{Type: agentapi.EventToolStep, Steps: []agentapi.ToolStep{{
			ID: "call_a", Name: "noop", Status: "ok",
		}}})
		tracker.Record(usage.Call{PromptTokens: prompt, CompletionTokens: 5})
		em.usageIfChanged(tracker)

		for _, ev := range pendingEvents(out) {
			if ev.Type == agentapi.EventToolStep && ev.Steps[0].Tokens != 0 {
				t.Fatalf("prompt %d: a non-growing context was given %d tokens", prompt, ev.Steps[0].Tokens)
			}
		}
	}
}

// TestUsageIfChangedRepeatedPendingCollapsesToOneEntry pins the ID matching: a
// pending step and the terminal step that replaces it are one invocation, not
// two, so the backfill emits one event rather than an extra row.
func TestUsageIfChangedRepeatedPendingCollapsesToOneEntry(t *testing.T) {
	out := make(chan agentapi.Event, 64)
	em := newRunEmitter(out)
	tracker := usage.New()

	tracker.Record(usage.Call{PromptTokens: 100, CompletionTokens: 20})
	em.send(agentapi.Event{Type: agentapi.EventToolStep, Steps: []agentapi.ToolStep{{
		ID: "call_a", Name: "search", Status: "pending",
	}}})
	em.send(agentapi.Event{Type: agentapi.EventToolStep, Steps: []agentapi.ToolStep{{
		ID: "call_a", Name: "search", Status: "ok", Duration: 50 * time.Millisecond,
	}}})
	tracker.Record(usage.Call{PromptTokens: 200, CompletionTokens: 10})
	em.usageIfChanged(tracker)

	backfilled := 0
	for _, ev := range pendingEvents(out) {
		if ev.Type == agentapi.EventToolStep && ev.Steps[0].Tokens > 0 {
			backfilled++
		}
	}
	if backfilled != 1 {
		t.Fatalf("backfilled %d rows for one invocation, want 1", backfilled)
	}
}

// TestUsageIfChangedNilTracker guards a run with no usage tracker.
func TestUsageIfChangedNilTracker(t *testing.T) {
	out := make(chan agentapi.Event, 8)
	em := newRunEmitter(out)
	em.usageIfChanged(nil)
	if got := pendingEvents(out); len(got) != 0 {
		t.Fatalf("got %+v, want nothing", got)
	}
}
