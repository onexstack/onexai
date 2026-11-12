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

package usage

import (
	"context"
	"testing"
)

func TestTrackerTotals(t *testing.T) {
	tr := New()
	tr.Record(Call{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, ReasoningTokens: 5})
	tr.Record(Call{PromptTokens: 400, CompletionTokens: 30, TotalTokens: 430, ReasoningTokens: 7})

	got := tr.Totals()
	want := Totals{Calls: 2, PromptTokens: 500, CompletionTokens: 50, TotalTokens: 550, ReasoningTokens: 12}
	if got != want {
		t.Fatalf("Totals() = %+v, want %+v", got, want)
	}
}

// TestTrackerEmptyTotals pins the shape a run that never reached the model
// reports: zero calls, so a renderer can tell "no data" from "zero tokens".
func TestTrackerEmptyTotals(t *testing.T) {
	if got := New().Totals(); got != (Totals{}) {
		t.Fatalf("Totals() = %+v, want the zero value", got)
	}
}

// TestTrackerNilSafe covers the optional-tracker path: the model adapter holds a
// tracker that may be absent, and must not need a guard of its own.
func TestTrackerNilSafe(t *testing.T) {
	var tr *Tracker
	tr.Record(Call{PromptTokens: 1})
	if got := tr.Totals(); got != (Totals{}) {
		t.Fatalf("nil Totals() = %+v, want the zero value", got)
	}
	if got := tr.Calls(); got != nil {
		t.Fatalf("nil Calls() = %v, want nil", got)
	}
}

// TestTrackerCallsReturnsCopy guards the snapshot contract: the run loop holds a
// Calls() result across records, so a later append must not write through it.
func TestTrackerCallsReturnsCopy(t *testing.T) {
	tr := New()
	tr.Record(Call{PromptTokens: 100})

	snapshot := tr.Calls()
	snapshot[0].PromptTokens = 999

	if got := tr.Calls()[0].PromptTokens; got != 100 {
		t.Fatalf("tracker saw the caller's mutation: PromptTokens = %d, want 100", got)
	}
}

func TestContextDelta(t *testing.T) {
	tests := []struct {
		name  string
		calls []Call
		i     int
		want  int
	}{
		{
			name: "tool result grew the next prompt",
			// Round 2's prompt holds round 1's prompt, round 1's answer (the
			// assistant message with the tool call) and the tool result. The
			// delta keeps only the result.
			calls: []Call{{PromptTokens: 100, CompletionTokens: 40}, {PromptTokens: 300}},
			i:     1,
			want:  160,
		},
		{
			name:  "assistant message accounts for all growth",
			calls: []Call{{PromptTokens: 100, CompletionTokens: 50}, {PromptTokens: 150}},
			i:     1,
			want:  0,
		},
		{
			name:  "prompt shrank",
			calls: []Call{{PromptTokens: 500, CompletionTokens: 40}, {PromptTokens: 300}},
			i:     1,
			want:  0,
		},
		{
			name:  "only one call so far",
			calls: []Call{{PromptTokens: 100, CompletionTokens: 40}},
			i:     0,
			want:  0,
		},
		{
			name:  "index past the end",
			calls: []Call{{PromptTokens: 100, CompletionTokens: 40}},
			i:     1,
			want:  0,
		},
		{
			name:  "no calls at all",
			calls: nil,
			i:     0,
			want:  0,
		},
		{
			name: "reads the pair around i, not the first pair",
			calls: []Call{
				{PromptTokens: 100, CompletionTokens: 10},
				{PromptTokens: 200, CompletionTokens: 20},
				{PromptTokens: 900, CompletionTokens: 30},
			},
			i:    2,
			want: 680,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContextDelta(tt.calls, tt.i); got != tt.want {
				t.Fatalf("ContextDelta() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTrackerContext(t *testing.T) {
	tr := New()
	ctx := WithTracker(context.Background(), tr)

	if got := FromContext(ctx); got != tr {
		t.Fatalf("FromContext() = %p, want the attached tracker %p", got, tr)
	}
}

// TestFromContextMissing pins that an untracked context yields nil rather than a
// fresh tracker: a tracker created on the fly would collect numbers nobody ever
// reads, hiding a wiring mistake.
func TestFromContextMissing(t *testing.T) {
	if got := FromContext(context.Background()); got != nil {
		t.Fatalf("FromContext() = %p, want nil", got)
	}
}
