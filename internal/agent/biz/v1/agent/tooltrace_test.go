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
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"

	"github.com/onexstack/onexai/internal/agent/pkg/agentbuilder"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// tracedStep runs one invocation through the handler and returns the terminal
// step it reported.
//
// maxBytes, when positive, lowers the handler's result bound so a truncation can
// be asserted without producing an output of the production size.
func tracedStep(t *testing.T, out string, callErr error, maxBytes int) agentapi.ToolStep {
	t.Helper()

	events := make(chan agentapi.Event, 8)
	h := newToolTraceHandler(newRunEmitter(events), 0)
	if maxBytes > 0 {
		h.maxResultBytes = maxBytes
	}

	endpoint, err := h.WrapInvokableToolCall(context.Background(),
		func(context.Context, string, ...tool.Option) (string, error) { return out, callErr },
		&adk.ToolContext{Name: "pipeline", CallID: "call_a"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall: %v", err)
	}
	if _, err := endpoint(context.Background(), "{}"); err != nil && !errors.Is(err, callErr) {
		t.Fatalf("endpoint: %v", err)
	}

	<-events // the pending step
	return (<-events).Steps[0]
}

// tracedTool runs one invocation through the handler and returns the events it
// reported. now advances by step on every read so the duration is deterministic
// without the test having to sleep.
func tracedTool(t *testing.T, callErr error, timeout time.Duration) []agentapi.Event {
	t.Helper()

	out := make(chan agentapi.Event, 8)
	h := newToolTraceHandler(newRunEmitter(out), timeout)
	tick := time.Unix(0, 0)
	h.now = func() time.Time {
		tick = tick.Add(250 * time.Millisecond)
		return tick
	}

	endpoint, err := h.WrapInvokableToolCall(context.Background(),
		func(context.Context, string, ...tool.Option) (string, error) { return "result", callErr },
		&adk.ToolContext{Name: "search", CallID: "call_a"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall: %v", err)
	}
	if _, err := endpoint(context.Background(), "{}"); !errors.Is(err, callErr) {
		t.Fatalf("endpoint returned %v, want the tool's own %v", err, callErr)
	}

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

// TestToolTraceHandlerReportsDuration pins the pair of events a successful call
// produces and the measured duration between them, keyed by the model's call ID
// so a renderer can update one row rather than add a second.
// The per-call deadline must be marked as such: it is the only way a timeout
// can be attributed to the tool's server rather than to the run running out of
// time. See contextx.WithToolTimeout and mcpclient's breaker.
func TestToolTraceHandlerMarksItsOwnDeadline(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    bool
	}{
		{name: "a configured timeout is marked", timeout: time.Minute, want: true},
		{name: "no timeout installs no deadline", timeout: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := make(chan agentapi.Event, 8)
			h := newToolTraceHandler(newRunEmitter(events), tt.timeout)

			var marked bool
			endpoint, err := h.WrapInvokableToolCall(context.Background(),
				func(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
					marked = contextx.IsToolTimeout(ctx)
					return "ok", nil
				},
				&adk.ToolContext{Name: "pipeline", CallID: "call_a"})
			if err != nil {
				t.Fatalf("WrapInvokableToolCall: %v", err)
			}
			if _, err := endpoint(context.Background(), "{}"); err != nil {
				t.Fatalf("endpoint: %v", err)
			}
			if marked != tt.want {
				t.Errorf("IsToolTimeout = %v, want %v", marked, tt.want)
			}
		})
	}
}

func TestToolTraceHandlerReportsDuration(t *testing.T) {
	got := tracedTool(t, nil, 0)
	if len(got) != 2 {
		t.Fatalf("got %d events, want a pending and a terminal one: %+v", len(got), got)
	}

	pending := got[0].Steps[0]
	if pending.Status != "pending" || pending.ID != "call_a" || pending.Name != "search" {
		t.Fatalf("pending step = %+v", pending)
	}
	if pending.Duration != 0 || pending.Tokens != 0 {
		t.Fatalf("pending step must not claim a duration or tokens: %+v", pending)
	}

	done := got[1].Steps[0]
	if done.Status != "ok" || done.ID != "call_a" {
		t.Fatalf("terminal step = %+v", done)
	}
	if done.Duration != 250*time.Millisecond {
		t.Fatalf("duration = %v, want 250ms", done.Duration)
	}
}

// TestToolTraceHandlerReportsFailure covers what the card could not show before:
// adk's own tool event sender returns without emitting when the endpoint fails,
// so a failed tool was invisible.
func TestToolTraceHandlerReportsFailure(t *testing.T) {
	boom := errors.New("tool exploded")
	got := tracedTool(t, boom, 0)
	if len(got) != 2 {
		t.Fatalf("got %d events, want a pending and a terminal one: %+v", len(got), got)
	}
	if st := got[1].Steps[0]; st.Status != "fail" || st.Duration != 250*time.Millisecond {
		t.Fatalf("terminal step = %+v, want a failure with its duration", st)
	}
}

// TestToolTraceHandlerKeepsPendingOnInterrupt pins that an approval pause is not
// mistaken for a failure: the call comes back as an error, but the run resumes
// it under the same call ID, so the row has to stay pending until then.
func TestToolTraceHandlerKeepsPendingOnInterrupt(t *testing.T) {
	pause := tool.StatefulInterrupt(context.Background(), "may I?", nil)
	if pause == nil {
		t.Fatal("tool.StatefulInterrupt returned nil")
	}
	if !agentbuilder.IsInterrupt(pause) {
		t.Fatalf("an interrupt signal is not recognised as one: %#v", pause)
	}

	got := tracedTool(t, pause, 0)
	if len(got) != 1 {
		t.Fatalf("got %d events, want the pending step alone: %+v", len(got), got)
	}
	if st := got[0].Steps[0]; st.Status != "pending" {
		t.Fatalf("interrupted step = %+v, want it left pending", st)
	}
}

// TestIsInterruptRejectsOrdinaryErrors keeps the interrupt test from swallowing
// real failures.
func TestIsInterruptRejectsOrdinaryErrors(t *testing.T) {
	if agentbuilder.IsInterrupt(nil) {
		t.Fatal("nil is not an interrupt")
	}
	if agentbuilder.IsInterrupt(errors.New("boom")) {
		t.Fatal("a plain error is not an interrupt")
	}
	if agentbuilder.IsInterrupt(context.DeadlineExceeded) {
		t.Fatal("a deadline is not an interrupt")
	}
}

// TestRoundUpToMillis covers the figures the card cannot show once truncated to
// zero. The wire carries whole milliseconds, and a zero there means "this
// backend measures nothing" — the local CLI backend reports every step that way
// — so a call that genuinely took a fraction of a millisecond must not be
// reported as unmeasured.
func TestRoundUpToMillis(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"a built-in tool's microseconds", 300 * time.Microsecond, time.Millisecond},
		{"a single nanosecond", time.Nanosecond, time.Millisecond},
		{"exactly a millisecond is left alone", time.Millisecond, time.Millisecond},
		{"a measurable call is untouched", 250 * time.Millisecond, 250 * time.Millisecond},
		{"just under a millisecond", 999 * time.Microsecond, time.Millisecond},
		{"an unmeasured call stays unmeasured", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := roundUpToMillis(tc.in); got != tc.want {
				t.Errorf("roundUpToMillis(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestToolTraceHandlerReportsFastCall checks the rounding at the point it must
// happen: through the handler, so a sub-millisecond tool still reaches the wire
// with a duration a renderer can show.
func TestToolTraceHandlerReportsFastCall(t *testing.T) {
	out := make(chan agentapi.Event, 8)
	h := newToolTraceHandler(newRunEmitter(out), 0)
	tick := time.Unix(0, 0)
	h.now = func() time.Time {
		tick = tick.Add(120 * time.Microsecond)
		return tick
	}

	endpoint, err := h.WrapInvokableToolCall(context.Background(),
		func(context.Context, string, ...tool.Option) (string, error) { return "ok", nil },
		&adk.ToolContext{Name: "current_time", CallID: "call_a"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall: %v", err)
	}
	if _, err := endpoint(context.Background(), "{}"); err != nil {
		t.Fatalf("endpoint: %v", err)
	}

	// Drain the pending step and read the terminal one.
	<-out
	st := (<-out).Steps[0]
	if st.Duration != time.Millisecond {
		t.Fatalf("duration = %v, want 1ms — a zero would render as no duration at all", st.Duration)
	}
	if st.Duration.Milliseconds() != 1 {
		t.Fatalf("the wire figure is %dms, want 1", st.Duration.Milliseconds())
	}
}

// TestToolTraceHandlerCarriesWhatTheToolReturned is the point of the result
// field: what a tool did becomes a fact the caller holds, rather than only
// something the model says happened. A run that claims to have triggered a
// release can then be checked against the release's own output on the same card.
func TestToolTraceHandlerCarriesWhatTheToolReturned(t *testing.T) {
	const body = `{"triggered":true,"task_id":39,"console_url":"https://zadig.example/v2"}`

	st := tracedStep(t, body, nil, 0)
	if st.Status != "ok" {
		t.Fatalf("status = %q, want ok", st.Status)
	}
	if st.Result != body {
		t.Errorf("result = %q, want the tool's own output verbatim", st.Result)
	}
}

// TestToolTraceHandlerCarriesAFailureMessage covers the path where the output is
// empty and the error is the only account of what happened — which is the case
// for a tool that reports a refusal by failing rather than by returning.
func TestToolTraceHandlerCarriesAFailureMessage(t *testing.T) {
	st := tracedStep(t, "", errors.New("zadig: 403 forbidden"), 0)
	if st.Status != "fail" {
		t.Fatalf("status = %q, want fail", st.Status)
	}
	if st.Result != "zadig: 403 forbidden" {
		t.Errorf("result = %q, want the failure's own message", st.Result)
	}
}

// TestToolTraceHandlerTruncatesALargeOutput keeps an unbounded tool from putting
// its whole output on every event of a run. The model still receives the output
// in full; only the copy that travels to a renderer is cut, and the cut is
// marked so it cannot be read as the whole of it.
func TestToolTraceHandlerTruncatesALargeOutput(t *testing.T) {
	const budget = 64
	st := tracedStep(t, strings.Repeat("x", 500), nil, budget)

	if len(st.Result) <= budget {
		t.Fatalf("result is %d bytes, want the %d-byte budget plus a marker", len(st.Result), budget)
	}
	if !strings.HasPrefix(st.Result, strings.Repeat("x", budget)) {
		t.Errorf("result does not begin with the output up to the budget: %q", st.Result)
	}
	if !strings.Contains(st.Result, "已截断") {
		t.Errorf("the cut is not marked, so a partial result reads as a whole one: %q", st.Result)
	}
}

// TestToolResultLeavesASmallOutputAlone is the counterweight: the bound exists to
// stop an unbounded output, not to reshape an ordinary one.
func TestToolResultLeavesASmallOutputAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"an ordinary result", "done", "done"},
		{"surrounding whitespace is trimmed", "  done\n", "done"},
		{"nothing at all", "", ""},
		{"only whitespace", "   \n\t", ""},
		{"a multi-line result keeps its shape", "line one\nline two", "line one\nline two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolResult(tc.in, nil, 4096); got != tc.want {
				t.Errorf("toolResult(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestToolResultCutsOnARuneBoundary keeps the cut from splitting a multi-byte
// character, which would put invalid UTF-8 into a JSON field of the event.
func TestToolResultCutsOnARuneBoundary(t *testing.T) {
	got := toolResult(strings.Repeat("中", 10), nil, 4)
	if !utf8.ValidString(got) {
		t.Fatalf("toolResult cut a character in half: %q", got)
	}
	if !strings.Contains(got, "已截断") {
		t.Errorf("result %q is not marked as cut", got)
	}
}

// TestToolTraceHandlerBoundsInvocation pins the configured timeout reaching the
// tool's context.
func TestToolTraceHandlerBoundsInvocation(t *testing.T) {
	out := make(chan agentapi.Event, 8)
	h := newToolTraceHandler(newRunEmitter(out), 30*time.Millisecond)

	var deadline time.Time
	var hasDeadline bool
	endpoint, err := h.WrapInvokableToolCall(context.Background(),
		func(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
			deadline, hasDeadline = ctx.Deadline()
			return "", nil
		},
		&adk.ToolContext{Name: "slow", CallID: "call_a"})
	if err != nil {
		t.Fatalf("WrapInvokableToolCall: %v", err)
	}
	if _, err := endpoint(context.Background(), "{}"); err != nil {
		t.Fatalf("endpoint: %v", err)
	}

	if !hasDeadline {
		t.Fatal("a configured tool timeout did not reach the tool's context")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 30*time.Millisecond {
		t.Fatalf("deadline is %v away, want within the 30ms bound", remaining)
	}
}
