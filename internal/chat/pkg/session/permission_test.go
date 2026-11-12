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

import (
	"context"
	"testing"
	"time"
)

func TestPendingPermissionRoundTrip(t *testing.T) {
	m, _ := testManager(t)
	s, err := m.Session("conv-1")
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}

	if got := s.PendingPermission(); got != nil {
		t.Fatalf("a new conversation already has a pending approval: %+v", got)
	}

	s.SetPendingPermission(&PendingPermission{
		RunID:    "run-1",
		ToolName: "Bash",
		Question: "run rm -rf /tmp/x?",
		AskedAt:  time.Now(),
	})

	got := s.PendingPermission()
	if got == nil {
		t.Fatal("PendingPermission() = nil after it was set")
	}
	if got.RunID != "run-1" || got.ToolName != "Bash" {
		t.Errorf("PendingPermission() = %+v, want the recorded run and tool", got)
	}

	// A reader must not be able to mutate what the conversation holds: the answer
	// path reads it and could otherwise rewrite the question it is answering.
	got.RunID = "tampered"
	if again := s.PendingPermission(); again.RunID != "run-1" {
		t.Errorf("a caller's edit to the returned struct changed the session's copy: %+v", again)
	}
}

func TestClearPendingPermissionIgnoresOtherRun(t *testing.T) {
	m, _ := testManager(t)
	s, err := m.Session("conv-1")
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	s.SetPendingPermission(&PendingPermission{RunID: "run-current"})

	// A late answer to a question that has been superseded must not erase the
	// question that replaced it.
	s.ClearPendingPermission("run-superseded")
	if s.PendingPermission() == nil {
		t.Fatal("clearing a different run cleared the pending approval")
	}

	s.ClearPendingPermission("run-current")
	if got := s.PendingPermission(); got != nil {
		t.Errorf("PendingPermission() = %+v after clearing its own run, want nil", got)
	}
}

// TestPendingPermissionNotPersisted pins the deliberate bound: an approval points
// at a run in another process, so a snapshot that outlived it would leave the
// conversation blocked on a question nobody can answer.
func TestPendingPermissionNotPersisted(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(context.Background(), Options{DataDir: dir})
	t.Cleanup(m.Close)

	s, err := m.Session("conv-1")
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	s.SetPendingPermission(&PendingPermission{RunID: "run-1"})
	if err := s.Update(func(st *State) { st.BackendSessionID = "cli-session-1" }); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	// A second manager over the same directory is what a restart looks like.
	restarted := NewManager(context.Background(), Options{DataDir: dir})
	t.Cleanup(restarted.Close)

	reloaded, err := restarted.Session("conv-1")
	if err != nil {
		t.Fatalf("Session returned error after restart: %v", err)
	}
	if got := reloaded.PendingPermission(); got != nil {
		t.Errorf("PendingPermission() = %+v after a restart, want nil", got)
	}
	if got := reloaded.State().BackendSessionID; got != "cli-session-1" {
		t.Errorf("BackendSessionID = %q after a restart, want the persisted value — "+
			"the rest of the state must still round-trip", got)
	}
}
