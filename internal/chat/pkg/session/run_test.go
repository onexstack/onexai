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

import "testing"

// TestCurrentRun pins what the conversation remembers about the turn it is
// running: a callback needs to tell "this card's run is the one in flight" from
// "this card is a leftover", and the backend cannot answer that.
func TestCurrentRun(t *testing.T) {
	m, _ := testManager(t)
	s, err := m.Session("oc_run")
	if err != nil {
		t.Fatal(err)
	}

	if got := s.CurrentRun(); got != "" {
		t.Errorf("CurrentRun() = %q on a fresh conversation, want empty", got)
	}

	s.SetCurrentRun("run-1")
	if got := s.CurrentRun(); got != "run-1" {
		t.Errorf("CurrentRun() = %q, want run-1", got)
	}

	// An empty id is not a run: recording it would leave the conversation
	// looking busy with nothing to name.
	s.SetCurrentRun("")
	if got := s.CurrentRun(); got != "run-1" {
		t.Errorf("CurrentRun() = %q after an empty SetCurrentRun, want run-1", got)
	}

	// A turn that ends after a newer one started must not clear the newer one's
	// claim: the card of the run actually in flight would then look like a
	// leftover and its stop button would refuse to work.
	s.SetCurrentRun("run-2")
	s.ClearCurrentRun("run-1")
	if got := s.CurrentRun(); got != "run-2" {
		t.Errorf("CurrentRun() = %q after clearing another run, want run-2", got)
	}

	s.ClearCurrentRun("run-2")
	if got := s.CurrentRun(); got != "" {
		t.Errorf("CurrentRun() = %q after clearing the running one, want empty", got)
	}
}

// TestCurrentRunIsPerConversation pins that one conversation's run does not
// answer for another's — the whole point of holding it per session.
func TestCurrentRunIsPerConversation(t *testing.T) {
	m, _ := testManager(t)

	first, err := m.Session("oc_first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Session("oc_second")
	if err != nil {
		t.Fatal(err)
	}

	first.SetCurrentRun("run-1")
	if got := second.CurrentRun(); got != "" {
		t.Errorf("CurrentRun() = %q on a conversation that never started a run, want empty", got)
	}
}
