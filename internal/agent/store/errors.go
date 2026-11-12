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

import (
	"gorm.io/gorm"
)

// ErrNotFound reports that a single-row read found no row.
//
// It is an alias of gorm.ErrRecordNotFound rather than a sentinel of its own:
// the standard store shape returns the ORM's not-found error, so a caller can
// write errors.Is(err, store.ErrNotFound) and it means the same thing as
// errors.Is(err, gorm.ErrRecordNotFound). Keeping it an alias means the
// control-plane store and the SQL store agree on the answer without a
// translation layer.
var ErrNotFound = gorm.ErrRecordNotFound
