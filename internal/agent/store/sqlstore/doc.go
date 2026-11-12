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

// Package sqlstore implements the standard agent table stores backed by a GORM
// database (MySQL or PostgreSQL, per the coredb dialect). List-valued
// fields are persisted as JSON text columns
// and converted to the backend-neutral model.* domain types at the boundary.
//
// # Timestamps are Unix milliseconds
//
// That is the unit every reader expects — onexai's own protos, and
// onex-console, which types both fields as Millis.
//
// The records have to say so, because for an int64 field named UpdatedAt GORM
// derives the unit from the field name alone and picks seconds. Left implicit,
// a millisecond value written by a handler was overwritten with a seconds one
// on every update, and the console rendered the result as 1970. Naming
// AutoUpdateTime selects the unit; the column belonged to the database either
// way, and still does.
//
// CreatedAt carries no tag on purpose: on insert its value comes from the
// caller, and GORM only fills it in when it is left at zero, which no caller in
// this service does.
package sqlstore
