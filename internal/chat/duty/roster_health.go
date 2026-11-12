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

package duty

import "sync"

// rosterHealth records whether a source could read the on-call rosters on its
// last Load. The Regen-backed sources embed it so both answer the store's
// rosterErrReporter question with one implementation instead of two.
//
// The lock is not for concurrent Loads: only the store's refresh goroutine
// calls Load. It is there because a command goroutine reads the verdict — via
// Store.RosterError — while that refresh may be running.
type rosterHealth struct {
	mu  sync.Mutex
	err error
}

// setRosterError records why the rosters could not be read. A nil error clears
// the verdict, which is what a pass that read every roster reports.
func (h *rosterHealth) setRosterError(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = err
}

// RosterError returns why the last Load could not read the on-call rosters, or
// nil when it could.
func (h *rosterHealth) RosterError() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}
