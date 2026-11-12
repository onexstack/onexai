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

package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

const (
	approveRunID     = "run-approve"
	approveRequestID = "req-approve"
)

// resumingEngine is both an Engine and a Resumable, which is what Approve
// requires. Run emits one answer and finishes, or blocks on gate when the test
// needs the conversation slot held; Resume records what the biz layer rebuilt
// and finishes.
type resumingEngine struct {
	gate chan struct{}

	mu      sync.Mutex
	resumes []agentapi.ResumeOptions
}

func (*resumingEngine) Name() string { return "resuming" }

func (e *resumingEngine) Run(_ context.Context, _ []agentapi.Message, _ agentapi.RunOptions) (<-chan agentapi.Event, error) {
	out := make(chan agentapi.Event, 2)
	go func() {
		defer close(out)
		out <- agentapi.Event{Type: agentapi.EventAnswer, Delta: "hello"}
		if e.gate != nil {
			<-e.gate
		}
		out <- agentapi.Event{Type: agentapi.EventDone, Final: "hello"}
	}()
	return out, nil
}

func (e *resumingEngine) Resume(_ context.Context, opts agentapi.ResumeOptions) (<-chan agentapi.Event, error) {
	e.mu.Lock()
	e.resumes = append(e.resumes, opts)
	e.mu.Unlock()
	out := make(chan agentapi.Event, 1)
	out <- agentapi.Event{Type: agentapi.EventDone, Final: "resumed"}
	close(out)
	return out, nil
}

func (e *resumingEngine) lastResume() (agentapi.ResumeOptions, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.resumes) == 0 {
		return agentapi.ResumeOptions{}, false
	}
	return e.resumes[len(e.resumes)-1], true
}

func (e *resumingEngine) resumeCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.resumes)
}

// newApproveBiz builds a biz with an in-memory checkpoint store, which is what
// makes Approve answerable without Redis.
func newApproveBiz(engine agentapi.Engine) (*AgentBiz, checkpoint.Store) {
	cps := checkpoint.New(nil, "", 0)
	return New(Deps{
		Engine:               engine,
		Sessions:             mem.NewSessionStore(nil, "test", 0),
		Stops:                runstop.NewLocal(),
		Checkpoints:          cps,
		MaxConcurrentPerConv: 1,
	}), cps
}

// seedSuspension records the metadata Approve rebuilds a run from.
func seedSuspension(t *testing.T, cps checkpoint.Store, sessionID string) {
	t.Helper()
	err := cps.PutMeta(t.Context(), approveRunID, agentapi.RunMeta{
		Agents:      []string{},
		Model:       "test-model",
		SessionID:   sessionID,
		UserID:      "user-1",
		Reasoning:   true,
		Source:      "lark",
		SenderName:  "User One",
		RequestID:   approveRequestID,
		AllowAll:    false,
		SubjectKind: agentapi.PrincipalKindUser,
		Turn:        1,
	})
	if err != nil {
		t.Fatalf("seed the suspension: %v", err)
	}
}

// TestAgentBiz_ApproveResumesTheRecordedRun is the primary resume path: the run
// is rebuilt from the checkpoint record (not from the caller), handed to the
// engine as the recorded identity, and the stream ends with the terminal event.
func TestAgentBiz_ApproveResumesTheRecordedRun(t *testing.T) {
	engine := &resumingEngine{}
	b, cps := newApproveBiz(engine)
	seedSuspension(t, cps, "session-approve")

	ch, err := b.Approve(t.Context(), &agentapi.ApproveRequest{
		RunID:     approveRunID,
		RequestID: approveRequestID,
		Decision:  agentapi.DecisionAllow,
	})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	events := drain(t, ch)
	if len(events) == 0 || events[len(events)-1].Type != agentapi.EventDone {
		t.Fatalf("the resumed stream ended with %v, want EventDone", events)
	}

	opts, ok := engine.lastResume()
	if !ok {
		t.Fatal("the engine was never resumed")
	}
	if opts.RequestID != approveRequestID {
		t.Errorf("resumed request %q, want %q", opts.RequestID, approveRequestID)
	}
	if opts.Decision != agentapi.DecisionAllow {
		t.Errorf("resumed decision %q, want %q", opts.Decision, agentapi.DecisionAllow)
	}
	// The identity comes off the checkpoint record, so these prove the record
	// was read rather than the request repeated.
	if opts.Run.RunID != approveRunID {
		t.Errorf("resumed run %q, want %q", opts.Run.RunID, approveRunID)
	}
	if opts.Run.SessionKey != "session-approve" {
		t.Errorf("resumed session %q, want %q", opts.Run.SessionKey, "session-approve")
	}
	if opts.Run.SenderID != "user-1" {
		t.Errorf("resumed sender %q, want %q", opts.Run.SenderID, "user-1")
	}
	if opts.Run.Model != "test-model" {
		t.Errorf("resumed model %q, want %q", opts.Run.Model, "test-model")
	}

	// Answering is one-shot: the suspension is gone now.
	if _, err := b.Approve(t.Context(), &agentapi.ApproveRequest{
		RunID:     approveRunID,
		RequestID: approveRequestID,
		Decision:  agentapi.DecisionAllow,
	}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("a second answer: want ErrNotPending, got %v", err)
	}
	if got := engine.resumeCount(); got != 1 {
		t.Fatalf("the engine was resumed %d times, want 1", got)
	}
}

// TestAgentBiz_ApproveRefusesAnUnknownOrStaleSuspension pins the two ways an
// answer can name nothing to resume.
func TestAgentBiz_ApproveRefusesAnUnknownOrStaleSuspension(t *testing.T) {
	t.Run("no stored checkpoint", func(t *testing.T) {
		b, _ := newApproveBiz(&resumingEngine{})
		if _, err := b.Approve(t.Context(), &agentapi.ApproveRequest{
			RunID: approveRunID, RequestID: approveRequestID, Decision: agentapi.DecisionAllow,
		}); !errors.Is(err, ErrNotPending) {
			t.Fatalf("want ErrNotPending, got %v", err)
		}
	})

	t.Run("request id does not match the pending pause", func(t *testing.T) {
		b, cps := newApproveBiz(&resumingEngine{})
		seedSuspension(t, cps, "session-stale")
		if _, err := b.Approve(t.Context(), &agentapi.ApproveRequest{
			RunID: approveRunID, RequestID: "some-later-request", Decision: agentapi.DecisionAllow,
		}); !errors.Is(err, ErrNotPending) {
			t.Fatalf("want ErrNotPending, got %v", err)
		}
	})
}

// TestAgentBiz_ApproveClaimIsOneShot pins the claim itself: two answers to the
// same pause must not both resume, however they were routed here.
func TestAgentBiz_ApproveClaimIsOneShot(t *testing.T) {
	b, cps := newApproveBiz(&resumingEngine{})
	seedSuspension(t, cps, "session-claim")

	req := &agentapi.ApproveRequest{RunID: approveRunID, RequestID: approveRequestID}
	if err := b.claim(t.Context(), req); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := b.claim(t.Context(), req); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second claim: want ErrNotPending, got %v", err)
	}
}

// TestAgentBiz_ApproveLeavesTheAnswerWhenTheConversationIsBusy pins the order
// Approve depends on: the conversation slot is taken before the answer is
// claimed, so a busy conversation must not consume the pause.
func TestAgentBiz_ApproveLeavesTheAnswerWhenTheConversationIsBusy(t *testing.T) {
	engine := &resumingEngine{gate: make(chan struct{})}
	b, cps := newApproveBiz(engine)
	seedSuspension(t, cps, "session-busy")

	askCh, err := b.Ask(t.Context(), &agentapi.AskRequest{
		SessionID: "session-busy",
		Input:     agentapi.Input{Text: "hold the slot"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if _, err := b.Approve(t.Context(), &agentapi.ApproveRequest{
		RunID: approveRunID, RequestID: approveRequestID, Decision: agentapi.DecisionAllow,
	}); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("Approve on a busy conversation: want ErrConversationBusy, got %v", err)
	}
	if got := engine.resumeCount(); got != 0 {
		t.Fatalf("a refused answer resumed the engine %d times", got)
	}

	close(engine.gate)
	drain(t, askCh)

	// The same answer must still be accepted: the busy refusal happened before
	// the claim, so it did not burn the pause.
	ch, err := b.Approve(t.Context(), &agentapi.ApproveRequest{
		RunID: approveRunID, RequestID: approveRequestID, Decision: agentapi.DecisionAllow,
	})
	if err != nil {
		t.Fatalf("Approve after the conversation freed up: %v", err)
	}
	drain(t, ch)
	if got := engine.resumeCount(); got != 1 {
		t.Fatalf("the engine was resumed %d times, want 1", got)
	}
}
