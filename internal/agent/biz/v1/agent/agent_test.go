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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runstop"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// gatedEngine emits an answer, waits for gate to close, then finishes. When
// gate is nil it finishes immediately.
type gatedEngine struct{ gate chan struct{} }

func (e *gatedEngine) Name() string { return "gated" }
func (e *gatedEngine) Run(_ context.Context, _ []agentapi.Message, _ agentapi.RunOptions) (<-chan agentapi.Event, error) {
	out := make(chan agentapi.Event, 4)
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

// ctxEngine blocks until the run context is cancelled, then reports Stopped.
type ctxEngine struct{}

func (ctxEngine) Name() string { return "ctx" }
func (ctxEngine) Run(ctx context.Context, _ []agentapi.Message, _ agentapi.RunOptions) (<-chan agentapi.Event, error) {
	out := make(chan agentapi.Event, 1)
	go func() {
		defer close(out)
		<-ctx.Done()
		out <- agentapi.Event{Type: agentapi.EventStopped}
	}()
	return out, nil
}

func drain(t *testing.T, ch <-chan agentapi.Event) []agentapi.Event {
	t.Helper()
	var evs []agentapi.Event
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-timeout:
			t.Fatal("timed out draining events")
		}
	}
}

func newBiz(engine agentapi.Engine, maxConc int) *AgentBiz {
	return New(Deps{
		Engine:               engine,
		Sessions:             mem.NewSessionStore(nil, "test", 0),
		Stops:                runstop.NewLocal(),
		MaxConcurrentPerConv: maxConc,
	})
}

// recordingEngine captures the RunOptions of the last run so a test can assert
// what the biz layer resolved before handing the run to an engine.
type recordingEngine struct{ got agentapi.RunOptions }

func (*recordingEngine) Name() string { return "recording" }
func (e *recordingEngine) Run(_ context.Context, _ []agentapi.Message, opts agentapi.RunOptions) (<-chan agentapi.Event, error) {
	e.got = opts
	out := make(chan agentapi.Event, 1)
	out <- agentapi.Event{Type: agentapi.EventDone, Final: "ok"}
	close(out)
	return out, nil
}

// TestAgentBiz_ResolvesReasoningPreference pins who decides whether the engine
// surfaces reasoning, because getting it backwards silently changes what the
// Lark card shows and neither failure is visible from the outside.
//
// The server default exists for callers that never mention reasoning (direct
// REST, TUI). A caller that did state a preference must keep it in both
// directions: a default of "on" must not revive the reasoning panel of a
// conversation that turned it off, since that panel does not exist on the card
// and updating it fails the whole preview.
func TestAgentBiz_ResolvesReasoningPreference(t *testing.T) {
	tests := []struct {
		name       string
		requested  bool
		set        bool
		byDefault  bool
		wantEngine bool
	}{
		{"unstated takes the server default (on)", false, false, true, true},
		{"unstated takes the server default (off)", false, false, false, false},
		{"an explicit yes survives a default of no", true, true, false, true},
		{"an explicit no survives a default of yes", false, true, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &recordingEngine{}
			b := New(Deps{
				Engine:               engine,
				Sessions:             mem.NewSessionStore(nil, "test", 0),
				Stops:                runstop.NewLocal(),
				MaxConcurrentPerConv: 1,
				DefaultReasoning:     tt.byDefault,
			})

			ch, err := b.Ask(context.Background(), &agentapi.AskRequest{
				SessionID:  "c1",
				Input:      agentapi.Input{Text: "hi"},
				Generation: agentapi.GenerationConfig{Reasoning: askOptionalBool(tt.set, tt.requested)},
			})
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}
			drain(t, ch)

			if engine.got.Reasoning != tt.wantEngine {
				t.Errorf("engine Reasoning = %v, want %v", engine.got.Reasoning, tt.wantEngine)
			}
		})
	}
}

// TestAgentBiz_ResolvesCompatApproval pins who decides what a gated tool does on
// a request that cannot be asked.
//
// The OpenAI-compatible endpoint starts runs with NoApprovalChannel set, and the
// three modes differ in ways that are only visible from the outside: "deny"
// changes what the model is told, "error" changes whether the request succeeds,
// and "allow" changes whether the gate exists at all. Only that endpoint consults
// the mode, so a chat run must stay gated however it is set — that asymmetry is
// the one a reader is most likely to get backwards.
func TestAgentBiz_ResolvesCompatApproval(t *testing.T) {
	const gated = "delete_cluster"

	tests := []struct {
		name         string
		mode         string
		noChannel    bool
		wantPatterns []string
		wantFallback string
	}{
		{"a chat run is gated whatever the compat mode says", customoptions.CompatApprovalAllow, false, []string{gated}, ""},
		{"a chat run carries no fallback", customoptions.CompatApprovalDeny, false, []string{gated}, ""},
		{"compat denies by default", "", true, []string{gated}, agentapi.ApprovalFallbackDeny},
		{"compat deny", customoptions.CompatApprovalDeny, true, []string{gated}, agentapi.ApprovalFallbackDeny},
		{"compat error", customoptions.CompatApprovalError, true, []string{gated}, agentapi.ApprovalFallbackError},
		// "allow" is expressed as an empty policy rather than as a mode the gate
		// reads: one enforcement path, and an ungated call is handed back as the
		// very endpoint it came in on.
		{"compat allow gates nothing", customoptions.CompatApprovalAllow, true, nil, ""},
		// The options validation refuses this at startup, so it can only arrive
		// programmatically. Refusing is the safe reading of a gate that cannot ask.
		{"an unknown mode refuses", "yolo", true, []string{gated}, agentapi.ApprovalFallbackDeny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &recordingEngine{}
			b := New(Deps{
				Engine:               engine,
				Sessions:             mem.NewSessionStore(nil, "test", 0),
				Stops:                runstop.NewLocal(),
				MaxConcurrentPerConv: 1,
				RequireApproval:      []string{gated},
				CompatApprovalMode:   tt.mode,
			})

			ch, err := b.Ask(context.Background(), &agentapi.AskRequest{
				SessionID: "c1",
				Input:     agentapi.Input{Text: "删掉测试集群"},
				Options: agentapi.ExecutionOptions{
					Approval: agentapi.Approval{Channel: askChannel(tt.noChannel)},
				},
			})
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}
			drain(t, ch)

			if !slices.Equal(engine.got.Approval, tt.wantPatterns) {
				t.Errorf("engine approval patterns = %v, want %v", engine.got.Approval, tt.wantPatterns)
			}
			if engine.got.ApprovalFallback != tt.wantFallback {
				t.Errorf("engine approval fallback = %q, want %q", engine.got.ApprovalFallback, tt.wantFallback)
			}
		})
	}
}

func TestAgentBiz_AskEmitsAnswerAndDone_AndPersists(t *testing.T) {
	b := newBiz(&gatedEngine{}, 1)
	ctx := context.Background()

	ch, err := b.Ask(ctx, &agentapi.AskRequest{SessionID: "c1", Input: agentapi.Input{Text: "hi"}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	evs := drain(t, ch)
	if len(evs) < 2 || evs[len(evs)-1].Type != agentapi.EventDone || evs[len(evs)-1].Final != "hello" {
		t.Fatalf("unexpected events: %+v", evs)
	}

	// The exchange (user + assistant) must be persisted for memory, followed by
	// the record of what the run called. This engine reports no tool steps, so
	// the record says exactly that: the absence has to be written down, or a
	// turn that recorded nothing and a turn that called nothing would look the
	// same to the next turn reading its own history back.
	msgs, _ := b.deps.Sessions.Load(ctx, "c1")
	if len(msgs) != 3 || msgs[0].Role != agentapi.RoleUser || msgs[1].Role != agentapi.RoleAssistant {
		t.Fatalf("session not persisted correctly: %+v", msgs)
	}
	trail := msgs[2]
	// RoleUser, deliberately: it is the only role that survives toSchemaMessages
	// into the model's next prompt, and an annotation the model cannot read
	// cannot stop it from repeating the claim it was meant to check.
	if trail.Role != agentapi.RoleUser {
		t.Errorf("tool record role = %q, want %q so the next turn actually sees it", trail.Role, agentapi.RoleUser)
	}
	if !strings.Contains(trail.Content, "没有调用任何工具") {
		t.Errorf("tool record = %q, want it to state that nothing was called", trail.Content)
	}
}

func TestAgentBiz_ConcurrencyLimit(t *testing.T) {
	gate := make(chan struct{})
	b := newBiz(&gatedEngine{gate: gate}, 1)
	ctx := context.Background()

	ch1, err := b.Ask(ctx, &agentapi.AskRequest{SessionID: "c1", Input: agentapi.Input{Text: "first"}})
	if err != nil {
		t.Fatalf("Ask#1: %v", err)
	}
	// Second concurrent request on the same conversation must be rejected.
	if _, err := b.Ask(ctx, &agentapi.AskRequest{SessionID: "c1", Input: agentapi.Input{Text: "second"}}); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("Ask#2: want ErrConversationBusy, got %v", err)
	}
	// A different conversation is unaffected.
	gate2 := make(chan struct{})
	close(gate2)
	if _, err := b.Ask(ctx, &agentapi.AskRequest{SessionID: "c2", Input: agentapi.Input{Text: "other"}}); err != nil {
		t.Fatalf("Ask(c2): %v", err)
	}

	close(gate) // let #1 finish
	drain(t, ch1)

	// After #1 completes, the conversation frees up again.
	ch3, err := b.Ask(ctx, &agentapi.AskRequest{SessionID: "c1", Input: agentapi.Input{Text: "third"}})
	if err != nil {
		t.Fatalf("Ask#3 after release: %v", err)
	}
	drain(t, ch3)
}

// A failure after the conversation slot is taken must release it. The failure
// used here is the invoked-skill check, which runs after admission and before
// the engine: Ask's cleanup guard is what unwinds the slot, so a regression
// shows up as a conversation that can never be asked again.
func TestAgentBiz_AskReleasesSlotOnPreflightFailure(t *testing.T) {
	b := newBiz(&gatedEngine{}, 1)
	ctx := context.Background()

	if _, err := b.Ask(ctx, &agentapi.AskRequest{
		SessionID: "c1",
		Selection: agentapi.Selection{InvokedSkills: []string{"not-declared"}},
	}); err == nil {
		t.Fatal("Ask with an undeclared skill: want an error, got nil")
	}

	ch, err := b.Ask(ctx, &agentapi.AskRequest{SessionID: "c1", Input: agentapi.Input{Text: "again"}})
	if err != nil {
		t.Fatalf("the preflight failure leaked the conversation slot: %v", err)
	}
	drain(t, ch)
}

func TestAgentBiz_Stop(t *testing.T) {
	b := newBiz(ctxEngine{}, 1)
	ctx := context.Background()

	ch, err := b.Ask(ctx, &agentapi.AskRequest{SessionID: "c1", Input: agentapi.Input{Text: "hi"}, RunID: "run-1"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if err := b.Stop(ctx, "run-1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	evs := drain(t, ch)
	if len(evs) == 0 || evs[len(evs)-1].Type != agentapi.EventStopped {
		t.Fatalf("want terminal EventStopped, got %+v", evs)
	}
}

// askOptionalBool preserves the tri-state the request uses: nil means "the
// caller did not say".
func askOptionalBool(set, v bool) *bool {
	if !set {
		return nil
	}
	return &v
}

func askChannel(none bool) agentapi.ApprovalChannel {
	if none {
		return agentapi.ApprovalChannelNone
	}
	return agentapi.ApprovalChannelUnspecified
}

// The conversation slot is a borrow, and the bookkeeping behind it must not
// outlive the borrow: the map used to grow by one channel per session this
// process had ever seen, which is a leak shaped like the busiest deployment.
func TestAcquireReclaimsTheConversationEntry(t *testing.T) {
	b := New(Deps{MaxConcurrentPerConv: 1})

	release, ok := b.acquire("c1")
	if !ok {
		t.Fatal("first acquire must succeed")
	}
	if _, ok := b.acquire("c1"); ok {
		t.Fatal("a second acquire for a full conversation must be refused")
	}
	release()
	release() // idempotent: a caller that also defers it must not release twice

	b.mu.Lock()
	entries := len(b.sems)
	b.mu.Unlock()
	if entries != 0 {
		t.Fatalf("the slot entry was not reclaimed: %d left", entries)
	}

	// And the slot is usable again, which is what a leaking entry would also
	// have allowed — the difference is the map, asserted above.
	release, ok = b.acquire("c1")
	if !ok {
		t.Fatal("the released conversation must admit the next run")
	}
	release()
}

// A deployment without a resource backend has no configured endpoints. That is
// an answer, not a panic: Deps.Resources is documented as nil-able.
func TestResolveModelsWithoutAResourceStore(t *testing.T) {
	b := New(Deps{})
	ctx := context.Background()

	requested, err := b.resolveRequestedModel(ctx, "user-model", "u1", false)
	if err != nil {
		t.Fatalf("a named endpoint with no store: %v", err)
	}
	if requested.found {
		t.Fatal("with no store nothing can be found")
	}
	if _, _, err := b.resolveRunModel(ctx, requested, resolvedSelection{}, "u1"); err != nil {
		t.Fatalf("falling back to the deployment default: %v", err)
	}
}

// A second pause records only its own pending addresses. Appending to the
// embedded record would accumulate the earlier ones, and a resumed run would
// then answer targets it never reached.
func TestSuspendMetaRecordsOnlyTheCurrentPending(t *testing.T) {
	sp := suspend{RunMeta: agentapi.RunMeta{SessionID: "s1", UserID: "u1"}, RunID: "r1"}
	first := sp.meta(&agentapi.Interrupt{
		RequestID: "req-1",
		Requests:  []agentapi.InterruptRequest{{Address: "a", Kind: "permission"}},
	}, []string{"tool-1"})
	if first.SessionID != "s1" || first.RequestID != "req-1" || len(first.ToolTrail) != 1 {
		t.Fatalf("the recorded run lost its identity: %+v", first)
	}

	second := sp.meta(&agentapi.Interrupt{
		RequestID: "req-2",
		Requests:  []agentapi.InterruptRequest{{Address: "b", Kind: "permission"}, {Address: "c", Kind: "question"}},
	}, []string{"tool-1", "tool-2"})
	if len(second.Pending) != 2 {
		t.Fatalf("pending addresses accumulated across pauses: %+v", second.Pending)
	}
	if len(first.Pending) != 1 || first.Pending[0].Address != "a" {
		t.Fatalf("the earlier record was mutated by the later pause: %+v", first.Pending)
	}
}
