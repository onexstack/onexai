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

// Page applies pagination to an already-materialised list.
//
// The cached store serves an unfiltered List from its snapshot, so paging that
// read is a slice rather than a query; this is the slice. A caller that passes
// a filter or a raw clause goes to the SQL store's own List, which counts and
// pages in the database, so nothing is silently ignored.
//
// A limit of zero or less means "no limit" and returns everything, which keeps
// the endpoint's behaviour identical to before pagination existed. The offset
// is ignored in that case, for the same reason: a client that asked for
// everything did not ask to start part-way through.
func Page[T any](items []T, offset, limit int) (int64, []T) {
	total := int64(len(items))
	if limit <= 0 {
		return total, items
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return total, []T{}
	}
	end := min(offset+limit, len(items))
	return total, items[offset:end]
}
