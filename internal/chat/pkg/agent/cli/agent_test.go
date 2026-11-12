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

package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
)

// TestCLIAgentRejectsUnusableOptions pins the two configurations that have no
// safe reading: a backend that cannot say where its agents run would run them in
// the server's own directory, and a session cap of zero is not a limit but a
// backend that refuses everything.
func TestCLIAgentRejectsUnusableOptions(t *testing.T) {
	if _, err := New(Options{MaxSessions: 1}); err == nil {
		t.Error("a backend with no workspace root was accepted")
	} else if !strings.Contains(err.Error(), "workspace root") {
		t.Errorf("error = %v, want it to name the missing workspace root", err)
	}

	if _, err := New(Options{WorkspaceRoot: t.TempDir()}); err == nil {
		t.Error("a backend with no session capacity was accepted")
	}
}

// TestCLIAgentSharesOneConversationPerKey pins the property the permission
// handshake depends on: the request that starts a turn and the callback that
// answers its approval are different requests, so asking for the conversation
// twice has to hand back the same object. Two objects would each hold their own
// pending request, and the answer would address a session nobody is blocked in.
func TestCLIAgentSharesOneConversationPerKey(t *testing.T) {
	a := newTestAgent(t, Options{WorkspaceRoot: t.TempDir(), MaxSessions: 2})
	ctx := context.Background()

	first, err := a.Session(ctx, "conv-a")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	again, err := a.Session(ctx, "conv-a")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if again != first {
		t.Error("the same key got two conversations")
	}

	other, err := a.Session(ctx, "conv-b")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if other == first {
		t.Error("two keys got the same conversation")
	}
	if got := a.Live(); got != 2 {
		t.Errorf("Live() = %d, want 2", got)
	}

	if _, err := a.Session(ctx, ""); err == nil {
		t.Error("a conversation with no key was accepted")
	}
}

// TestCLIAgentRefusesMoreThanMaxSessions pins the capacity limit as a refusal
// rather than a queue. Each conversation is a process on this host, so a request
// that waited for a slot would hold a chat message open for a process's whole
// lifetime — and the caller can only tell the user to retry if it is told.
func TestCLIAgentRefusesMoreThanMaxSessions(t *testing.T) {
	a := newTestAgent(t, Options{WorkspaceRoot: t.TempDir(), MaxSessions: 1})
	ctx := context.Background()

	if _, err := a.Session(ctx, "conv-a"); err != nil {
		t.Fatalf("Session: %v", err)
	}

	_, err := a.Session(ctx, "conv-b")
	if !errors.Is(err, agent.ErrTooManySessions) {
		t.Fatalf("error = %v, want ErrTooManySessions", err)
	}

	// A conversation that is over frees its slot: the limit is on live
	// conversations, not on how many a deployment has ever had.
	if err := a.CloseSession(ctx, "conv-a"); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if _, err := a.Session(ctx, "conv-b"); err != nil {
		t.Fatalf("Session after releasing a slot: %v", err)
	}

	// Closing an unknown conversation is not an error: conversations are reaped on
	// paths that do not track which backend holds what.
	if err := a.CloseSession(ctx, "never-existed"); err != nil {
		t.Errorf("CloseSession(unknown) = %v, want nil", err)
	}
}

// TestCLIAgentCloseEndsEveryConversation pins that a backend shutting down
// releases its processes and stops handing out conversations. Handing one out
// after that would start a process this backend would never close.
func TestCLIAgentCloseEndsEveryConversation(t *testing.T) {
	a, err := New(Options{WorkspaceRoot: t.TempDir(), MaxSessions: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if _, err := a.Session(ctx, "conv-a"); err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := a.Live(); got != 0 {
		t.Errorf("Live() = %d after Close, want 0", got)
	}
	if _, err := a.Session(ctx, "conv-a"); err == nil {
		t.Error("a closed backend handed out a conversation")
	}
	// Closing twice is how a shutdown path and a defer meet.
	if err := a.Close(); err != nil {
		t.Errorf("a second Close = %v, want nil", err)
	}
}
