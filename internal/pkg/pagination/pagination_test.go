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

package pagination

import "testing"

func TestPageClamps(t *testing.T) {
	tests := []struct {
		page, size         int
		wantPage, wantSize int
	}{
		{0, 0, 1, DefaultPageSize},
		{-1, -5, 1, DefaultPageSize},
		{1, 1, 1, 1},
		{3, 50, 3, 50},
		{2, MaxPageSize + 1, 2, MaxPageSize},
	}
	for _, tt := range tests {
		page, size := Page(tt.page, tt.size)
		if page != tt.wantPage || size != tt.wantSize {
			t.Errorf("Page(%d, %d) = (%d, %d), want (%d, %d)",
				tt.page, tt.size, page, size, tt.wantPage, tt.wantSize)
		}
	}
}

func TestOffsetLimitClamps(t *testing.T) {
	tests := []struct {
		offset, limit         int
		wantOffset, wantLimit int
	}{
		{-1, 10, 0, 10},
		{0, 0, 0, 0},
		{0, -1, 0, -1},
		{5, MaxListLimit + 1, 5, MaxListLimit},
	}
	for _, tt := range tests {
		offset, limit := OffsetLimit(tt.offset, tt.limit)
		if offset != tt.wantOffset || limit != tt.wantLimit {
			t.Errorf("OffsetLimit(%d, %d) = (%d, %d), want (%d, %d)",
				tt.offset, tt.limit, offset, limit, tt.wantOffset, tt.wantLimit)
		}
	}
}
