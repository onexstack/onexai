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
	"time"

	"github.com/onexstack/onexstack/pkg/ptr"
)

// PendingPermission is an approval this conversation is waiting on: an agent run
// paused at a human-approval interrupt that the user has not answered.
//
// It is in memory only, and that is a deliberate bound rather than an omission.
// The run it points at lives in another process; if that process restarts, the
// run is gone and an answer would resume nothing. A persisted copy would survive
// the only thing that could act on it, and a conversation stuck waiting for an
// approval nobody is listening for is worse than one that admits the run ended.
type PendingPermission struct {
	// RunID identifies the paused run. Answering resumes this run and no other.
	RunID string
	// RequestID identifies the pause being answered, as the backend reported it.
	// One run can pause more than once, and this is what makes an answer apply to
	// the pause the human was looking at rather than to whatever the run waits on
	// by the time the answer is delivered. It is empty for a backend that names no
	// pause — the CLI — which answers the one it holds.
	RequestID string
	// Kind is the interrupt kind the run paused on: "question" for an
	// AskUserQuestion prompt, anything else for a tool approval. It decides how a
	// typed reply is interpreted — a question takes free text as its answer, an
	// approval accepts only a recognisable decision.
	Kind string
	// ToolName is the tool the run asked to use, or the tools it is waiting on
	// when the pause covers a group, for showing the user what they are
	// approving.
	ToolName string
	// Question is what the run asked, in plain text.
	Question string
	// Address is the interrupt point a typed answer targets, for a question. It
	// is empty for an approval, which addresses none: the decision covers the
	// group.
	Address string
	// AskedBy is the identity of whoever triggered the turn — the same value the
	// backend received as the sender. An approval is their decision to make, so
	// this is what a click from someone else is checked against.
	AskedBy string
	// AskedAt is when the pause was recorded.
	AskedAt time.Time
}

// SetPendingPermission records the approval this conversation is waiting on,
// replacing any earlier one: a run that asks again has superseded its own
// question.
func (s *Session) SetPendingPermission(p *PendingPermission) {
	if p == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = ptr.Clone(p)
}

// PendingPermission returns the approval this conversation is waiting on, or nil
// when there is none.
func (s *Session) PendingPermission() *PendingPermission {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil
	}
	return ptr.Clone(s.pending)
}

// ClearPendingPermission forgets the approval identified by runID, and does
// nothing when a different run is the one being waited on.
//
// The run ID is why this is not just "clear it": an answer to a question that has
// since been superseded would otherwise erase the question that replaced it, and
// the conversation would have no record of the approval it is actually blocked on.
func (s *Session) ClearPendingPermission(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil || (runID != "" && s.pending.RunID != runID) {
		return
	}
	s.pending = nil
}
