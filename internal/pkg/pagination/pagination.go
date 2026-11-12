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

// Package pagination clamps the two list shapes the APIs expose.
//
// A page/pageSize endpoint uses Page; a proto offset/limit endpoint uses
// Where. They are deliberately separate: a caller that sent a page number is
// not the same caller as one that sent an offset, and one spelling must not be
// guessed from the other.
package pagination

import "github.com/onexstack/onexstack/pkg/store/where"

const (
	// DefaultPageSize is what a page request that names no size gets.
	DefaultPageSize = 20
	// MaxPageSize is the largest page a caller may ask for.
	MaxPageSize = 200
	// MaxListLimit caps a positive offset/limit request.
	MaxListLimit = 200
)

// Page clamps a page/pageSize pair: a page below 1 is read as 1, a size below
// 1 as DefaultPageSize, and a size above MaxPageSize as MaxPageSize.
func Page(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	return page, size
}

// OffsetLimit clamps an offset/limit pair: a negative offset is read as 0, and
// a positive limit above MaxListLimit is capped. A non-positive limit is left
// alone because the store reads it as unbounded.
func OffsetLimit(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	return offset, limit
}

// Where returns list options for a proto offset/limit request, clamped.
func Where(offset, limit int) *where.Options {
	offset, limit = OffsetLimit(offset, limit)
	return where.NewWhere().O(offset).L(limit)
}
