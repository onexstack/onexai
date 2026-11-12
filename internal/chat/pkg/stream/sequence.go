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

// Package stream holds the streaming primitives shared by the oai-chat-apiserver
// output paths: a strictly increasing sequence counter, and a throttle that
// decides when a partially-generated answer is worth publishing.
//
// It depends on nothing else in this repository. What it exists to hold are two
// invariants that are easy to get wrong and expensive to diagnose — that
// incremental updates are strictly ordered, and that a slow or failing transport
// costs smoothness but never content — and both are properties of the algorithm
// rather than of any one platform.
package stream

import "sync"

// Sequence hands out strictly increasing numbers for one streaming target.
//
// The platform rejects an update whose sequence does not exceed the previous
// one, and rejects it silently: nothing tells the caller, and the target simply
// keeps showing the older text. A single writer doing `seq++` survives that. The
// moment a second writer exists — a mirror to the TUI, a fallback renderer — a
// shared counter incremented by hand produces duplicates under ordinary
// interleaving, and the symptom is a card that stops updating for no visible
// reason. So the counter is shared, and the only way to obtain a number is to
// ask for one.
type Sequence struct {
	mu sync.Mutex
	n  int64
}

// NewSequence returns a counter at zero. The zero value works too; this exists so
// a call site does not have to spell out the pointer to a struct it may not
// otherwise mention.
func NewSequence() *Sequence { return &Sequence{} }

// Next returns the next number. It never returns the same value twice and never
// goes backwards — including after a caller's update failed, because a failure
// may well have happened after the platform applied the update, in which case
// reusing the number would be rejected as non-increasing.
func (s *Sequence) Next() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.n
}

// Current returns the last number handed out, or 0 if none has been. It does not
// advance the counter.
func (s *Sequence) Current() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}
