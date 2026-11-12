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
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/agentbuilder"
	"github.com/onexstack/onexai/internal/agent/pkg/approval"
	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authz"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// gatedToolName is the tool these tests gate. It is also the pattern they put in
// the run's approval list, so a rename has to reach both.
const gatedToolName = "delete_cluster"

// TestResolveApproval covers the union the whole feature rests on: a pattern
// declared on an MCP server row holds that server's tool for every agent
// allowed to use it, and no other source can release it again.
//
// The rows here mirror the live deployment's: the chat server publishes
// /pipeline, and listing it on that row is the entire declaration — the tool
// itself is untouched, and so is every run that does not select that server.
func TestResolveApproval(t *testing.T) {
	ctx := context.Background()

	resources := &fakeAIStore{
		AIStore: store.NewEmptyStore(),
		agents: map[string]*model.AgentM{
			"ops":   {Name: "ops", MCPs: []string{"chat"}},
			"other": {Name: "other", MCPs: []string{"files"}},
			"own":   {Name: "own", RequireApproval: []string{"own_tool"}},
			"none":  {Name: "none"},
		},
		mcps: map[string]*model.MCPM{
			"chat":  {Name: "chat", RequireApproval: []string{"pipeline"}},
			"files": {Name: "files"},
		},
	}

	tests := []struct {
		name     string
		agents   []string
		fallback []string
		want     []string
	}{
		{"a server's pattern follows its tools", []string{"ops"}, nil, []string{"pipeline"}},
		{"a server that gates nothing contributes nothing", []string{"other"}, nil, nil},
		{"an agent's own patterns are added", []string{"own"}, nil, []string{"own_tool"}},
		{"the server-wide default is always included", []string{"ops"}, []string{"deploy"}, []string{"deploy", "pipeline"}},
		{"no agent selected leaves the default alone", nil, []string{"deploy"}, []string{"deploy"}},
		{"an agent without servers contributes nothing", []string{"none"}, nil, nil},
		{"an unknown agent is skipped rather than fatal", []string{"nope", "ops"}, nil, []string{"pipeline"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewResolver(resources, tt.fallback).Resolve(ctx, tt.agents).Approval
			if !slices.Equal(got, tt.want) {
				t.Errorf("resolveApproval(agents=%v, fallback=%v) = %v, want %v",
					tt.agents, tt.fallback, got, tt.want)
			}
		})
	}
}

// TestResolvedPolicyHoldsTheNamedTool closes the loop the previous test opens:
// what resolveApproval returns has to reach approval.Policy and land as
// DecisionAsk for the tool the model actually calls.
//
// It is written against the model-facing name on purpose. The row names a tool
// the way a human reads it, and agent matches it after sanitising the wire
// name, so a declaration can only be checked against the name the model sees.
func TestResolvedPolicyHoldsTheNamedTool(t *testing.T) {
	ctx := context.Background()
	resources := &fakeAIStore{
		AIStore: store.NewEmptyStore(),
		agents: map[string]*model.AgentM{
			"default": {Name: "default", MCPs: []string{"oai-chat-apiserver"}},
		},
		mcps: map[string]*model.MCPM{
			"oai-chat-apiserver": {Name: "oai-chat-apiserver", RequireApproval: []string{"pipeline"}},
		},
	}

	patterns := NewResolver(resources, nil).Resolve(ctx, []string{"default"}).Approval
	policy := approval.New(patterns, nil)

	if got := policy.Decide("pipeline"); got != approval.DecisionAsk {
		t.Errorf("Decision(%q) = %v, want DecisionAsk", "pipeline", got)
	}
	// The neighbouring tools of the same server are gated only if named, which
	// is what keeps one release gate from holding every read-only command too.
	for _, name := range []string{"chat", "cronjob_list", "mark"} {
		if got := policy.Decide(name); got != approval.DecisionAllow {
			t.Errorf("Decision(%q) = %v, want DecisionAllow", name, got)
		}
	}
}

// gatedArgs is the parameter struct of the gated tool. A named parameter is what
// makes the arguments worth preserving across the pause: an empty object would
// come back through even a checkpoint that lost them, so the tests below assert
// on the value the tool was finally run with.
type gatedArgs struct {
	Cluster string `json:"cluster"`
}

// gateSpy records how a gated tool was called. What it was called with is the
// evidence that the pause preserved the arguments and that only the approved
// call ran.
type gateSpy struct {
	mu   sync.Mutex
	seen []string
}

func (s *gateSpy) invoke(args gatedArgs) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, args.Cluster)
	return "deleted " + args.Cluster
}

func (s *gateSpy) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// newGatedTool builds the tool the approval tests gate and observe.
func newGatedTool(t *testing.T, spy *gateSpy) tool.BaseTool {
	t.Helper()
	gated, err := utils.InferTool(gatedToolName, "Delete a Kubernetes cluster.",
		func(_ context.Context, args gatedArgs) (string, error) { return spy.invoke(args), nil })
	if err != nil {
		t.Fatalf("InferTool: %v", err)
	}
	return gated
}

// approvalUpstream is an OpenAI-compatible upstream that plays back one canned
// SSE body per call — a tool call first, then an answer — and records every
// request body it was sent. The recorded body of the round after a refusal is
// how a test shows what the model was actually told: a refusal handed back as a
// tool result is only meaningful if the model reads it.
type approvalUpstream struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
	rounds []string
}

func newApprovalUpstream(t *testing.T, rounds ...string) *approvalUpstream {
	t.Helper()
	if len(rounds) == 0 {
		t.Fatal("newApprovalUpstream needs at least one round")
	}
	u := &approvalUpstream{rounds: rounds}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, string(raw))
		round := len(u.bodies)
		u.mu.Unlock()

		if round > len(u.rounds) {
			round = len(u.rounds)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, u.rounds[round-1])
	}))
	t.Cleanup(u.Server.Close)
	return u
}

// calls is how many model calls the upstream has served.
func (u *approvalUpstream) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

// sentToModel is the request body of the given round, 1-based.
func (u *approvalUpstream) sentToModel(t *testing.T, round int) string {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) < round {
		t.Fatalf("the model was called %d times, want at least %d", len(u.bodies), round)
	}
	return u.bodies[round-1]
}

// toolCallDelta is the delta that asks for one tool call in a streamed round.
//
// The index is what tells the two calls of a parallel batch apart: it identifies
// the call a delta belongs to, so two deltas sharing one are the same call —
// which would collapse a pair into a single interrupt point and quietly turn the
// test below into a test of nothing.
func toolCallDelta(index int, id, name, args string) string {
	return `{"tool_calls":[{"index":` + strconv.Itoa(index) + `,"id":"` + id + `","type":"function","function":{"name":"` +
		name + `","arguments":` + strconv.Quote(args) + `}}]}`
}

// approvalEngine builds the eino engine over a memory checkpoint store. The
// store is not optional here: without one the middleware refuses to gate
// anything at all, which TestApprovalHandlerRefusesToGateWithoutACheckpointStore
// covers on its own.
func approvalEngine(t *testing.T, url string, tools ...tool.BaseTool) agentapi.Engine {
	t.Helper()
	eng := NewEngine(EngineConfig{
		BaseURL:     url,
		Model:       "test-model",
		Tools:       func([]string, []string) []tool.BaseTool { return tools },
		Checkpoints: checkpoint.New(nil, "onex", time.Minute),
		Reserved:    []string{"ask_user_question"},
	})
	if eng == nil {
		t.Fatal("NewEngine returned nil for a configured backend")
	}
	return eng
}

// gatedRun is the run options every test below starts from: one gated tool, one
// named run.
func gatedRun(runID string) agentapi.RunOptions {
	return agentapi.RunOptions{Approval: []string{gatedToolName}, RunID: runID}
}

func approvalMsgs() []agentapi.Message {
	return []agentapi.Message{{Role: agentapi.RoleUser, Content: "删掉测试集群"}}
}

// pause is what a test needs out of a suspended run.
type pause struct {
	interrupt *agentapi.Interrupt
	requests  []agentapi.InterruptRequest
}

// drainToPause drains a run that is expected to pause for a decision.
//
// A suspension is not an ending, and the assertions here are what hold the two
// apart: the stream must end with EventSuspended, and must not carry a Done,
// Failed or Stopped — a card that finalized on a pause would present a run
// waiting for a human as a finished one.
func drainToPause(t *testing.T, ch <-chan agentapi.Event) pause {
	t.Helper()
	var (
		got   pause
		seen  int
		last  agentapi.EventType
		count = 0
	)
	for _, ev := range drain(t, ch) {
		count++
		last = ev.Type
		switch ev.Type {
		case agentapi.EventInterrupted:
			seen++
			got.interrupt = ev.Interrupt
		case agentapi.EventDone, agentapi.EventFailed, agentapi.EventStopped:
			t.Fatalf("a run that paused also reported %v: %+v", ev.Type, ev)
		}
	}
	if seen != 1 {
		t.Fatalf("the run raised %d interrupts, want exactly 1 (events: %d)", seen, count)
	}
	if last != agentapi.EventSuspended {
		t.Fatalf("the paused stream ended with %v, want EventSuspended", last)
	}
	if got.interrupt == nil || len(got.interrupt.Requests) == 0 {
		t.Fatalf("the interrupt describes no request: %+v", got.interrupt)
	}
	got.requests = got.interrupt.Requests
	return got
}

// drainToAnswer drains a run that is expected to finish, returning the answer it
// closed on. Pausing again would mean the gate had not been answered, which is a
// failed test rather than a short answer.
func drainToAnswer(t *testing.T, ch <-chan agentapi.Event) string {
	t.Helper()
	final := ""
	suspended := false
	for _, ev := range drain(t, ch) {
		switch ev.Type {
		case agentapi.EventDone:
			final = ev.Final
		case agentapi.EventInterrupted:
			suspended = true
		case agentapi.EventFailed:
			t.Fatalf("the run failed: %v", ev.Err)
		}
	}
	if suspended {
		t.Fatal("the run paused again instead of finishing")
	}
	return final
}

// TestApprovalHandlerPausesThenRunsOnApproval is the primary path: a gated call
// interrupts instead of running, the run suspends without ending, and the answer
// continues it — executing exactly the call that paused, with the arguments it
// paused with.
func TestApprovalHandlerPausesThenRunsOnApproval(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
		streamReply(`{"content":"已删除"}`),
	)
	eng := approvalEngine(t, up.URL, newGatedTool(t, spy))
	runs := gatedRun("approve-allow")

	ch, err := eng.Run(t.Context(), approvalMsgs(), runs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drainToPause(t, ch)

	if len(got.requests) != 1 {
		t.Fatalf("the pause names %d requests, want 1: %+v", len(got.requests), got.requests)
	}
	req := got.requests[0]
	if req.ToolName != gatedToolName {
		t.Errorf("the pause is for %q, want %q", req.ToolName, gatedToolName)
	}
	if req.Kind != agentapi.InterruptKindPermission {
		t.Errorf("the pause is of kind %q, want %q", req.Kind, agentapi.InterruptKindPermission)
	}
	if req.Address == "" {
		t.Error("the pause names no address, so nothing could target it")
	}
	// The approver decides on the arguments, not on the tool's name alone: a
	// prompt that hides them invites approving a delete of the wrong cluster.
	if !strings.Contains(req.Question, gatedToolName) || !strings.Contains(req.Question, "test") {
		t.Errorf("the prompt %q does not show the tool and its arguments", req.Question)
	}

	// Nothing ran and no second model round happened: the pause is the whole
	// effect of the call so far.
	if calls := spy.calls(); len(calls) != 0 {
		t.Fatalf("the gated tool ran before it was approved: %v", calls)
	}
	if n := up.calls(); n != 1 {
		t.Fatalf("the model was called %d times before the approval, want 1", n)
	}

	resumable, ok := eng.(agentapi.Resumable)
	if !ok {
		t.Fatal("the eino engine cannot resume a suspended run")
	}
	resumed, err := resumable.Resume(t.Context(), agentapi.ResumeOptions{
		Run:       runs,
		RequestID: got.interrupt.RequestID,
		Decision:  agentapi.DecisionAllow,
		Pending: []agentapi.PendingAddress{
			{Address: req.Address, Kind: req.Kind, ToolName: req.ToolName},
		},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if answer := drainToAnswer(t, resumed); answer != "已删除" {
		t.Errorf("the resumed run answered %q, want %q", answer, "已删除")
	}
	if calls := spy.calls(); len(calls) != 1 || calls[0] != "test" {
		t.Errorf("the tool ran with %v, want exactly [test]", calls)
	}
}

// TestApprovalHandlerHandsARefusalToTheModel pins the semantics of a denial: the
// tool does not run, and the model is told so as an ordinary tool result. It is
// not an error and not a dead run — the turn continues, which is the only way
// the refusal reaches the person who asked for the work.
func TestApprovalHandlerHandsARefusalToTheModel(t *testing.T) {
	const reason = "the cluster is still serving traffic"

	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
		streamReply(`{"content":"好的，已取消删除。"}`),
	)
	eng := approvalEngine(t, up.URL, newGatedTool(t, spy))
	runs := gatedRun("approve-deny")

	ch, err := eng.Run(t.Context(), approvalMsgs(), runs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drainToPause(t, ch)

	resumed, err := eng.(agentapi.Resumable).Resume(t.Context(), agentapi.ResumeOptions{
		Run:       runs,
		RequestID: got.interrupt.RequestID,
		Decision:  agentapi.DecisionDeny,
		Reason:    reason,
		Pending: []agentapi.PendingAddress{
			{Address: got.requests[0].Address, Kind: got.requests[0].Kind, ToolName: got.requests[0].ToolName},
		},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// A refusal is an answer: the run finishes rather than failing.
	if answer := drainToAnswer(t, resumed); answer != "好的，已取消删除。" {
		t.Errorf("the refused run answered %q, want the model's own closing text", answer)
	}
	if calls := spy.calls(); len(calls) != 0 {
		t.Fatalf("a refused tool ran anyway: %v", calls)
	}

	// The model's second round has to carry the refusal, or the model would be
	// answering as if the delete had happened.
	round := up.sentToModel(t, 2)
	if !strings.Contains(round, gatedToolName) || !strings.Contains(round, reason) {
		t.Errorf("the refusal did not reach the model; round 2 was:\n%s", round)
	}
}

// TestApprovalHandlerRefusesABareResume covers the branch the engine itself
// never produces: an address named as the resume target with no answer attached.
// eino documents a nil target as "no data needed", which is fine for a question
// and cannot be read as consent here — running the tool would execute a
// destructive call on the strength of an absence.
func TestApprovalHandlerRefusesABareResume(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
		streamReply(`{"content":"已取消。"}`),
	)
	eng := approvalEngine(t, up.URL, newGatedTool(t, spy)).(*einoEngine)
	runs := gatedRun("approve-bare")

	ctx := t.Context()
	events := startRun(t, eng, ctx, runs)
	paused, ok := waitForPause(t, events)
	if !ok {
		t.Fatal("the run finished without pausing")
	}

	// One target, no data: eino marks the address as resumed and hands the
	// middleware a target it has no answer for.
	iter, err := eng.resumeRunner(t, ctx, runs).ResumeWithParams(ctx, runs.RunID, &adk.ResumeParams{
		Targets: map[string]any{paused[0].ID: nil},
	})
	if err != nil {
		t.Fatalf("ResumeWithParams: %v", err)
	}
	drainIterator(t, iter)

	if calls := spy.calls(); len(calls) != 0 {
		t.Fatalf("an unanswered resume ran the gated tool: %v", calls)
	}
	if round := up.sentToModel(t, 2); !strings.Contains(round, gatedToolName) {
		t.Errorf("the model was not told the tool was refused; round 2 was:\n%s", round)
	}
}

// TestApprovalHandlerAnswersOnlyWhatItWasAskedTo is the parallel-call case that
// makes a group decision worth having. One assistant message calls the same
// gated tool twice; answering one of them has to run one and leave the other
// pending, which eino requires of an untargeted leaf — answering a group in part
// replays what was already approved, and dropping the rest would abandon a call
// nobody decided on.
func TestApprovalHandlerAnswersOnlyWhatItWasAskedTo(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(
			toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"alpha"}`),
			toolCallDelta(1, "call_2", gatedToolName, `{"cluster":"beta"}`),
		),
		streamReply(`{"content":"done"}`),
	)
	eng := approvalEngine(t, up.URL, newGatedTool(t, spy)).(*einoEngine)
	runs := gatedRun("approve-group")

	ctx := t.Context()
	events := startRun(t, eng, ctx, runs)
	paused, ok := waitForPause(t, events)
	if !ok {
		t.Fatal("the run finished without pausing")
	}
	if len(paused) != 2 {
		t.Fatalf("the pause names %d interrupt points, want 2", len(paused))
	}

	// Answer the call for "alpha" alone. Which address is which is read from the
	// payload rather than assumed from the order.
	alpha := ictxForCluster(t, paused, "alpha")
	iter, err := eng.resumeRunner(t, ctx, runs).ResumeWithParams(ctx, runs.RunID, &adk.ResumeParams{
		Targets: map[string]any{alpha.ID: &agentapi.ApprovalResult{Approved: true}},
	})
	if err != nil {
		t.Fatalf("ResumeWithParams: %v", err)
	}

	next, ok := waitForPause(t, adkEvents(t, iter))
	if !ok {
		t.Fatal("the run finished with a call still unanswered")
	}
	if len(next) != 1 {
		t.Fatalf("the second pause names %d interrupt points, want 1: %+v", len(next), next)
	}
	if got := permissionOf(t, next[0]); got.ToolName != gatedToolName {
		t.Errorf("the second pause is for %q, want %q", got.ToolName, gatedToolName)
	}

	if calls := spy.calls(); len(calls) != 1 || calls[0] != "alpha" {
		t.Errorf("the tool ran with %v, want exactly the answered call [alpha]", calls)
	}
}

// TestApprovalHandlerRefusesToGateWithoutACheckpointStore is the fail-closed
// configuration: a policy that gates something while no store can hold the
// suspension has to fail the run, because the alternative is a gate that appears
// configured in the resources and enforces nothing.
func TestApprovalHandlerRefusesToGateWithoutACheckpointStore(t *testing.T) {
	spy := &gateSpy{}
	eng := NewEngine(EngineConfig{
		BaseURL: "http://127.0.0.1:1",
		Model:   "test-model",
		Tools:   func([]string, []string) []tool.BaseTool { return []tool.BaseTool{newGatedTool(t, spy)} },
	})
	if eng == nil {
		t.Fatal("NewEngine returned nil for a configured backend")
	}

	ch, err := eng.Run(t.Context(), approvalMsgs(), gatedRun("approve-no-store"))
	if err == nil {
		t.Fatalf("a gated run without a checkpoint store started anyway: %+v", drain(t, ch))
	}
	if !strings.Contains(err.Error(), gatedToolName) {
		t.Errorf("the failure %q does not name the tool that could not be gated", err)
	}
	if calls := spy.calls(); len(calls) != 0 {
		t.Errorf("the tool ran despite the run failing: %v", calls)
	}
}

// TestApprovalHandlerLeavesAnUngatedCallAlone covers the other half of the
// decision: a tool the policy does not name runs straight through, with no
// checkpoint, no interrupt and no answer needed. A gate that also paused
// ungated tools would make every tool call require a human.
func TestApprovalHandlerLeavesAnUngatedCallAlone(t *testing.T) {
	endpoint := func(_ context.Context, args string, _ ...tool.Option) (string, error) {
		return "ran with " + args, nil
	}

	tests := []struct {
		name     string
		policy   approval.Policy
		allowAll bool
		tool     string
		wantRuns bool
	}{
		{"a tool the policy does not name runs", approval.New([]string{gatedToolName}, nil), false, "list_agent", true},
		{"a tool the policy names pauses", approval.New([]string{gatedToolName}, nil), false, gatedToolName, false},
		// A reserved tool is one whose own contract is to consult a human. Gating
		// it would ask a human whether to ask a human.
		{"a reserved tool runs even when its name matches", approval.New([]string{"*"}, []string{"ask_user_question"}),
			false, "ask_user_question", true},
		// Allow-all is answered once for the whole run, so the resumed run must
		// stop asking — including on a different pod, which is why the flag is
		// rebuilt from the suspension rather than remembered.
		{"allow-all stops the asking", approval.New([]string{gatedToolName}, nil), true, gatedToolName, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newApprovalHandler(tt.policy, tt.allowAll, "")
			wrapped, err := h.WrapInvokableToolCall(t.Context(), endpoint, &adk.ToolContext{Name: tt.tool})
			if err != nil {
				t.Fatalf("WrapInvokableToolCall: %v", err)
			}
			if wrapped == nil {
				t.Fatal("the call was dropped rather than passed through")
			}

			out, err := wrapped(t.Context(), "{}")
			switch {
			case tt.wantRuns && err != nil:
				t.Fatalf("an ungated call failed: %v", err)
			case tt.wantRuns && out != "ran with {}":
				t.Fatalf("an ungated call returned %q, want the tool's own result", out)
			case !tt.wantRuns && !agentbuilder.IsInterrupt(err):
				t.Fatalf("a gated call returned (%q, %v), want an interrupt", out, err)
			}
		})
	}
}

// TestApprovalHandlerRefusesWhatItCannotAskAbout pins the two fallbacks a
// surface with no way to ask runs with. The gate still refuses to run the call —
// what changes is that it stops there instead of raising a question, because an
// interrupt on such a request would end the stream with a prompt nobody can
// answer, and the caller would receive an empty answer for work that never ran.
func TestApprovalHandlerRefusesWhatItCannotAskAbout(t *testing.T) {
	ran := false
	endpoint := func(_ context.Context, args string, _ ...tool.Option) (string, error) {
		ran = true
		return "ran with " + args, nil
	}

	tests := []struct {
		name     string
		fallback string
		wantErr  bool
	}{
		{"deny hands the model a refusal", agentapi.ApprovalFallbackDeny, false},
		{"error fails the call", agentapi.ApprovalFallbackError, true},
		// An empty fallback is the ordinary pausable run. The contrast is the
		// point: the same tool, the same policy, and only this field differs.
		{"no fallback pauses", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ran = false
			h := newApprovalHandler(approval.New([]string{gatedToolName}, nil), false, tt.fallback)
			wrapped, err := h.WrapInvokableToolCall(t.Context(), endpoint, &adk.ToolContext{Name: gatedToolName})
			if err != nil {
				t.Fatalf("WrapInvokableToolCall: %v", err)
			}

			out, callErr := wrapped(t.Context(), `{"cluster":"test"}`)
			if ran {
				t.Fatal("a gated call ran without an answer")
			}
			switch {
			case tt.fallback == "":
				if !agentbuilder.IsInterrupt(callErr) {
					t.Fatalf("a pausable gated call returned (%q, %v), want an interrupt", out, callErr)
				}
			case tt.wantErr:
				if callErr == nil || !strings.Contains(callErr.Error(), gatedToolName) {
					t.Fatalf("the call returned (%q, %v), want a failure naming the tool", out, callErr)
				}
			default:
				if callErr != nil {
					t.Fatalf("a refused call failed instead of returning a result: %v", callErr)
				}
				if !strings.Contains(out, gatedToolName) {
					t.Fatalf("the refusal %q does not name the tool", out)
				}
			}
		})
	}
}

// TestApprovalHandlerNeverParksARunThatCannotBeResumed is the same fallback seen
// from the outside, through a whole run.
//
// This is the shape the OpenAI-compatible endpoint produces: it starts runs with
// ApprovalFallback set, because a caller of that endpoint has no card to click
// and no approve call to make. Whatever mode is configured, the run has to end —
// either with the model's account of the refusal or with a failure — rather than
// suspend on a question that request could never put to anyone.
func TestApprovalHandlerNeverParksARunThatCannotBeResumed(t *testing.T) {
	tests := []struct {
		name     string
		fallback string
		wantFail bool
	}{
		{"deny", agentapi.ApprovalFallbackDeny, false},
		{"error", agentapi.ApprovalFallbackError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &gateSpy{}
			up := newApprovalUpstream(t,
				streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
				streamReply(`{"content":"该操作需要人工确认，我没有执行"}`),
			)
			eng := approvalEngine(t, up.URL, newGatedTool(t, spy))
			runs := gatedRun("run_compat_" + tt.name)
			runs.ApprovalFallback = tt.fallback

			events, err := eng.Run(t.Context(), approvalMsgs(), runs)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			var (
				answer   string
				runErr   error
				sawPause bool
				sawDone  bool
			)
			for _, ev := range drain(t, events) {
				switch ev.Type {
				case agentapi.EventDone:
					answer, sawDone = ev.Final, true
				case agentapi.EventFailed:
					runErr = ev.Err
				case agentapi.EventInterrupted, agentapi.EventSuspended:
					sawPause = true
				}
			}

			if sawPause {
				t.Fatal("a run that cannot be resumed parked on a question anyway")
			}
			if got := spy.calls(); len(got) != 0 {
				t.Fatalf("the gated tool ran without an answer: %v", got)
			}

			if tt.wantFail {
				if runErr == nil || !strings.Contains(runErr.Error(), gatedToolName) {
					t.Fatalf("the run ended with (%q, %v), want a failure naming the tool", answer, runErr)
				}
				if sawDone {
					t.Fatal("a failed run also reported Done")
				}
				return
			}

			if runErr != nil {
				t.Fatalf("the run failed instead of explaining the refusal: %v", runErr)
			}
			if !strings.Contains(answer, "人工确认") {
				t.Fatalf("the run answered %q, want the model's account of the refusal", answer)
			}
			// The refusal has to reach the model, not just the caller: the model
			// is the one that explains it, and it can only do that if the tool
			// node handed it a result.
			if body := up.sentToModel(t, 2); !strings.Contains(body, gatedToolName) {
				t.Errorf("the second model call does not carry the refusal:\n%s", body)
			}
		})
	}
}

// TestApprovalHandlerRefusesToolsThatCannotPause pins the wrapper paths that
// have nowhere to put a pause. Nothing in this deployment streams today; these
// exist so that adding a streamable tool to a server whose name matches an
// approval pattern fails loudly at assembly rather than running unapproved
// while the resources claim otherwise.
//
// The enhanced *invokable* path is deliberately absent: it can pause, so it is
// gated rather than refused — see TestApprovalHandlerGatesEnhancedCalls.
func TestApprovalHandlerRefusesToolsThatCannotPause(t *testing.T) {
	h := newApprovalHandler(approval.New([]string{gatedToolName}, nil), false, "")
	tCtx := &adk.ToolContext{Name: gatedToolName}

	wrap := map[string]func() error{
		"streamable": func() error {
			_, err := h.WrapStreamableToolCall(t.Context(), nil, tCtx)
			return err
		},
		"enhanced streamable": func() error {
			_, err := h.WrapEnhancedStreamableToolCall(t.Context(), nil, tCtx)
			return err
		},
	}

	for kind, call := range wrap {
		t.Run(kind, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("a gated tool was wrapped down a path that cannot pause for it")
			}
			if !strings.Contains(err.Error(), gatedToolName) {
				t.Errorf("the failure %q does not name the tool", err)
			}
		})
	}

	// The same paths still hand an ungated tool its endpoint, so the refusal is
	// about the gate rather than about the tool shape.
	if _, err := h.WrapStreamableToolCall(t.Context(), nil, &adk.ToolContext{Name: "list_agent"}); err != nil {
		t.Errorf("an ungated streamable tool was refused: %v", err)
	}
}

// TestApprovalHandlerGatesEnhancedCalls pins that an enhanced tool — the shape
// multimodal read_file has — goes through the same gate as a plain one: a gated
// call pauses instead of running, and an ungated one is handed straight through.
func TestApprovalHandlerGatesEnhancedCalls(t *testing.T) {
	h := newApprovalHandler(approval.New([]string{gatedToolName}, nil), false, "")

	called := false
	endpoint := func(context.Context, *schema.ToolArgument, ...tool.Option) (*schema.ToolResult, error) {
		called = true
		return &schema.ToolResult{Parts: []schema.ToolOutputPart{{
			Type: schema.ToolPartTypeText, Text: "ok",
		}}}, nil
	}

	gated, err := h.WrapEnhancedInvokableToolCall(t.Context(), endpoint,
		&adk.ToolContext{Name: gatedToolName, CallID: "call_1"})
	if err != nil {
		t.Fatalf("a gated enhanced tool was refused instead of gated: %v", err)
	}
	if _, err := gated(t.Context(), &schema.ToolArgument{Text: `{"path":"diagram.png"}`}); err == nil {
		t.Fatal("a gated enhanced call ran without pausing for approval")
	} else if !agentbuilder.IsInterrupt(err) {
		t.Fatalf("a gated enhanced call failed with %v, want an interrupt", err)
	}
	if called {
		t.Fatal("the tool ran before it was approved")
	}

	ungated, err := h.WrapEnhancedInvokableToolCall(t.Context(), endpoint,
		&adk.ToolContext{Name: "list_agent", CallID: "call_2"})
	if err != nil {
		t.Fatalf("wrapping an ungated enhanced tool failed: %v", err)
	}
	out, err := ungated(t.Context(), &schema.ToolArgument{Text: "{}"})
	if err != nil {
		t.Fatalf("an ungated enhanced call failed: %v", err)
	}
	if !called || out == nil || len(out.Parts) == 0 {
		t.Fatalf("an ungated enhanced call did not run: out=%+v called=%v", out, called)
	}
}

// TestApprovalHandlerRefusalText pins the words the model reads in place of a
// tool's result. They have to name the tool, forbid a retry — otherwise the
// model's natural next move is to call the same tool again — and carry the
// approver's reason when there is one, since the reason is the whole value of
// asking a human.
func TestApprovalHandlerRefusalText(t *testing.T) {
	plain := refused(gatedToolName, &agentapi.ApprovalResult{Approved: false})
	if !strings.Contains(plain, gatedToolName) {
		t.Errorf("the refusal %q does not name the tool", plain)
	}
	if !strings.Contains(plain, "不要重试") {
		t.Errorf("the refusal %q does not forbid a retry", plain)
	}

	const reason = "the cluster is still serving traffic"
	withReason := refused(gatedToolName, &agentapi.ApprovalResult{Approved: false, Reason: reason})
	if !strings.Contains(withReason, reason) {
		t.Errorf("the refusal %q drops the approver's reason", withReason)
	}

	// A refusal recorded with nothing attached — the bare-resume branch — still
	// has to read as a refusal rather than as an empty answer.
	bare := refused(gatedToolName, nil)
	if !strings.Contains(bare, gatedToolName) || !strings.Contains(bare, "拒绝") {
		t.Errorf("the refusal %q does not read as one", bare)
	}
}

// ---- helpers that drive adk directly ----

// startRun begins a run whose iterator this test drives itself, which is what
// lets it resume with a hand-built target set. The engine's Run cannot: it
// always resumes a group from the addresses the suspension recorded.
func startRun(t *testing.T, e *einoEngine, ctx context.Context, runs agentapi.RunOptions) <-chan *adk.AgentEvent {
	t.Helper()
	out := make(chan agentapi.Event, 8)
	discard(t, out)
	runCtx, runner, _, err := e.prepare(ctx, runs, false, newRunEmitter(out))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return adkEvents(t, runner.Run(runCtx, toSchemaMessages("", approvalMsgs()), adk.WithCheckPointID(runs.RunID)))
}

// resumeRunner builds the runner a resume needs. It must be assembled from the
// same options the pause recorded, since the checkpoint names its interrupt
// points by address.
func (e *einoEngine) resumeRunner(t *testing.T, ctx context.Context, runs agentapi.RunOptions) *adk.Runner {
	t.Helper()
	out := make(chan agentapi.Event, 8)
	discard(t, out)
	_, runner, _, err := e.prepare(ctx, runs, false, newRunEmitter(out))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return runner
}

// adkEvents pumps an iterator in the background. adk's Next blocks, so a run
// that stops making progress would otherwise hang the package until go test's
// own timeout instead of failing here.
func adkEvents(t *testing.T, iter *adk.AsyncIterator[*adk.AgentEvent]) <-chan *adk.AgentEvent {
	t.Helper()
	out := make(chan *adk.AgentEvent)
	go func() {
		defer close(out)
		for {
			ev, ok := iter.Next()
			if !ok {
				return
			}
			out <- ev
		}
	}()
	return out
}

// waitForPause reads until the run pauses, returning the interrupt points it is
// waiting on. It reports false when the run ended without pausing.
func waitForPause(t *testing.T, events <-chan *adk.AgentEvent) ([]*adk.InterruptCtx, bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return nil, false
			}
			if ev == nil {
				continue
			}
			if ev.Err != nil {
				t.Fatalf("the run failed: %v", ev.Err)
			}
			if ev.Action == nil || ev.Action.Interrupted == nil {
				continue
			}
			var roots []*adk.InterruptCtx
			for _, ictx := range ev.Action.Interrupted.InterruptContexts {
				if ictx != nil && ictx.IsRootCause {
					roots = append(roots, ictx)
				}
			}
			return roots, true
		case <-deadline:
			t.Fatal("timed out waiting for the run to pause")
			return nil, false
		}
	}
}

// drainIterator reads an iterator to its end, failing on an error. It is how a
// test lets a resumed run finish when it is not what is under test.
func drainIterator(t *testing.T, iter *adk.AsyncIterator[*adk.AgentEvent]) {
	t.Helper()
	for ev := range adkEvents(t, iter) {
		if ev != nil && ev.Err != nil {
			t.Fatalf("the resumed run failed: %v", ev.Err)
		}
	}
}

// ictxForCluster returns the interrupt point whose payload describes a call to
// the named cluster, so a test names a call by what it does rather than by the
// order adk happened to report it in.
func ictxForCluster(t *testing.T, points []*adk.InterruptCtx, cluster string) *adk.InterruptCtx {
	t.Helper()
	for _, ictx := range points {
		if info, ok := ictx.Info.(*agentapi.PermissionInfo); ok && strings.Contains(info.Arguments, cluster) {
			return ictx
		}
	}
	t.Fatalf("no interrupt point describes a call for %q", cluster)
	return nil
}

// permissionOf reads the permission payload out of an interrupt point.
func permissionOf(t *testing.T, ictx *adk.InterruptCtx) *agentapi.PermissionInfo {
	t.Helper()
	info, ok := ictx.Info.(*agentapi.PermissionInfo)
	if !ok {
		t.Fatalf("the interrupt point carries %T, want a permission payload", ictx.Info)
	}
	return info
}

// discard drains a channel for the rest of the test. A run emits into it whether
// or not anyone is listening, and a full channel would block the tool goroutine
// rather than fail the test.
func discard(t *testing.T, ch <-chan agentapi.Event) {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-ch:
			case <-done:
				return
			}
		}
	}()
}

// askingToolName is a tool whose contract is ask_user_question's: it raises its
// **own** interrupt and, on resume, returns the text the user typed. It is
// deliberately not in the run's approval policy — the point of the test below is
// what the middleware does with an interrupt it never raised.
const askingToolName = "asking_tool"

type askingArgs struct {
	Question string `json:"question"`
}

// newAskingTool builds that tool, recording the answers it was resumed with.
func newAskingTool(t *testing.T, resumedWith *[]string) tool.BaseTool {
	t.Helper()
	asking, err := utils.InferTool(askingToolName, "Ask the user a question and wait.",
		func(ctx context.Context, args askingArgs) (string, error) {
			wasInterrupted, _, _ := tool.GetInterruptState[any](ctx)
			if !wasInterrupted {
				return "", tool.Interrupt(ctx, &agentapi.AskQuestionInfo{Question: args.Question})
			}
			isTarget, hasData, data := tool.GetResumeContext[string](ctx)
			if !isTarget {
				return "", tool.Interrupt(ctx, nil)
			}
			if hasData {
				*resumedWith = append(*resumedWith, data)
				return data, nil
			}
			return "", errors.New("resumed without an answer")
		})
	if err != nil {
		t.Fatalf("InferTool: %v", err)
	}
	return asking
}

// TestApprovalHandlerLeavesAToolsOwnQuestionAlone pins a defect that inverted an
// entire feature into a lie about the user.
//
// # What used to happen
//
// Once an authorization gate is attached, this middleware wraps *every* tool
// call. `ask_user_question` raises an interrupt of its own, and the resume path
// read it with `GetResumeContext[*ApprovalResult]`: the human's answer is a
// `string`, so `hasData` was false, the `case isTarget:` branch answered
// `refused`, and the model was told **「用户拒绝执行工具 ask_user_question」**
// (the user refused to run tool ask_user_question) moments after the person had
// answered it. The tool never ran, the answer was
// dropped, and the model — reading a refusal — wrote prose instead of using what
// it had been given. Observed live on 2026-10-13 while wiring OneX's console
// assistant; it affects every surface that asks a question.
//
// # What has to hold
//
// The answer reaches the tool, and the model's next request carries that answer
// rather than a refusal. Both are asserted: the tool's own record alone would
// pass even if the result never reached the model, which is the other half of
// what a user sees.
func TestApprovalHandlerLeavesAToolsOwnQuestionAlone(t *testing.T) {
	var resumedWith []string
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_q", askingToolName, `{"question":"你想学什么"}`)),
		streamReply(`{"content":"收到"}`),
	)
	// An *enabled* gate is what makes the handler wrap every call, so it is what
	// makes this case reachable at all. It allows everything: the call is held by
	// the question's own interrupt, not by authorization.
	coord := testCoordinator(
		staticAuthorizer("tool", authz.DecisionAllow, ""),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)
	eng := authzEngine(t, up.URL, coord, newAskingTool(t, &resumedWith))
	runs := ungatedRun("own-question")

	ch, err := eng.Run(userCtx(), approvalMsgs(), runs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drainToPause(t, ch)
	req := got.requests[0]
	if req.Kind != agentapi.InterruptKindQuestion {
		t.Fatalf("the pause is of kind %q, want %q", req.Kind, agentapi.InterruptKindQuestion)
	}

	resumable, ok := eng.(agentapi.Resumable)
	if !ok {
		t.Fatal("the eino engine cannot resume a suspended run")
	}
	const answer = "MARKER77 我想学 Go 的并发模型"
	resumed, err := resumable.Resume(userCtx(), agentapi.ResumeOptions{
		Run:       runs,
		RequestID: got.interrupt.RequestID,
		Decision:  agentapi.DecisionAllow,
		Answers:   map[string]string{req.Address: answer},
		Pending: []agentapi.PendingAddress{
			{Address: req.Address, Kind: req.Kind, ToolName: req.ToolName},
		},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	drainToAnswer(t, resumed)

	if len(resumedWith) != 1 || resumedWith[0] != answer {
		t.Fatalf("the tool was resumed with %v, want exactly [%q]", resumedWith, answer)
	}

	last := up.bodies[len(up.bodies)-1]
	if !strings.Contains(last, answer) {
		t.Errorf("the model's next request does not carry the answer it was given: %s", last)
	}
	if strings.Contains(last, "用户拒绝执行工具") {
		t.Errorf("the model was told the user refused the tool they had just answered: %s", last)
	}
}
