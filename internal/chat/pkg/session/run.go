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

package session

// The run this conversation is currently executing.
//
// It exists so a card callback can tell whether the card it was clicked on
// still belongs to the turn in flight. A card outlives its run — the stop
// button stays clickable after the answer has finished — and without this the
// only way to find out is to ask the backend, where the answer is "accepted"
// whether or not anything was there to stop. The LLM backend cannot distinguish
// the two at all: its stop is addressed by run id alone, and a run id nobody
// holds is still a well-formed request.
//
// Like PendingPermission this is in memory only, and for the same reason: the
// run lives in another process, so a persisted copy would survive the only
// thing that could act on it and would describe a run that is already gone.

// SetCurrentRun records the run this conversation is now executing.
func (s *Session) SetCurrentRun(runID string) {
	if runID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentRun = runID
}

// CurrentRun returns the run this conversation is now executing, or "" when it
// is idle — or when this process never saw one start.
func (s *Session) CurrentRun() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentRun
}

// ClearCurrentRun forgets the current run when runID is the one that is
// running.
//
// The run id is why this is not simply "clear it": turns end out of order. A
// turn that was interrupted, or that outlived a later one, must not erase the
// record of the run it no longer owns — otherwise the card of the turn actually
// in flight would look like a leftover and its stop button would refuse to
// work.
func (s *Session) ClearCurrentRun(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if runID != "" && s.currentRun != runID {
		return
	}
	s.currentRun = ""
}
