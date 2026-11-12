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

package store

import "testing"

// TestPage pins the backward-compatibility rule the list endpoints rest on: a
// request that does not ask for a page gets everything, so adding pagination
// does not change what an existing client receives.
func TestPage(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}

	tests := []struct {
		name      string
		offset    int
		limit     int
		wantTotal int64
		wantPage  string
	}{
		{name: "no limit returns everything", limit: 0, wantTotal: 5, wantPage: "abcde"},
		{name: "negative limit returns everything", limit: -1, wantTotal: 5, wantPage: "abcde"},
		{name: "offset ignored when there is no limit", offset: 3, limit: 0, wantTotal: 5, wantPage: "abcde"},
		{name: "first page", limit: 2, wantTotal: 5, wantPage: "ab"},
		{name: "second page", offset: 2, limit: 2, wantTotal: 5, wantPage: "cd"},
		{name: "short last page", offset: 4, limit: 2, wantTotal: 5, wantPage: "e"},
		{name: "offset past the end is empty, not an error", offset: 5, limit: 2, wantTotal: 5, wantPage: ""},
		{name: "negative offset clamps to the start", offset: -3, limit: 2, wantTotal: 5, wantPage: "ab"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, page := Page(items, tt.offset, tt.limit)
			if total != tt.wantTotal {
				t.Errorf("total = %d, want %d", total, tt.wantTotal)
			}
			got := ""
			for _, s := range page {
				got += s
			}
			if got != tt.wantPage {
				t.Errorf("page = %q, want %q", got, tt.wantPage)
			}
		})
	}
}
