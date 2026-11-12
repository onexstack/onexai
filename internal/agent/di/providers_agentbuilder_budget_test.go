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

package di

import "testing"

// TestRetrievalCharsBudget pins the relationship between a knowledge base's
// rendering budget and the engine's tool-output cap.
//
// The two numbers are configured in different sections and neither reader sees
// both, so this is the only place the invariant is stated. It matters because
// the cap is applied last: a rendering budget above it is not the number in
// effect, and the cut the cap makes is the one that loses middle-ranked
// evidence without the tool's own "(more results omitted)" marker.
func TestRetrievalCharsBudget(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		cap        int
		want       int
	}{
		{
			// The shape this guard exists for: a budget 2.5x the cap, which is
			// what a deployment that reads the two settings as independent
			// numbers writes.
			name:       "above the cap is pulled under it",
			configured: 30000, cap: 12000,
			want: 10800,
		},
		{
			name:       "already below the cap is left alone",
			configured: 8000, cap: 20000,
			want: 8000,
		},
		{
			name:       "exactly at the cap is treated as above it",
			configured: 12000, cap: 12000,
			want: 10800,
		},
		{
			// "No cap configured" is not the same as a cap of zero: the guard
			// is off, so there is no relationship to enforce and the budget
			// must keep the operator's number.
			name:       "no cap leaves the budget untouched",
			configured: 30000, cap: 0,
			want: 30000,
		},
		{
			name:       "no budget configured stays disabled",
			configured: 0, cap: 12000,
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retrievalCharsBudget(tt.configured, tt.cap); got != tt.want {
				t.Errorf("retrievalCharsBudget(%d, %d) = %d, want %d",
					tt.configured, tt.cap, got, tt.want)
			}
		})
	}
}

// TestRetrievalCharsBudgetIsAlwaysRenderableByTheTool guards the direction the
// guard must never take: the effective budget has to stay strictly under the
// cap, or the context guard reaches the retrieval result and the whole point is
// lost.
func TestRetrievalCharsBudgetIsAlwaysRenderableByTheTool(t *testing.T) {
	const cap = 16000
	for _, configured := range []int{1, 1000, cap - 1, cap, cap + 1, cap * 4} {
		if got := retrievalCharsBudget(configured, cap); got >= cap {
			t.Errorf("retrievalCharsBudget(%d, %d) = %d, which the tool-output cap would still trim",
				configured, cap, got)
		}
	}
}
