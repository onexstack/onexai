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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// noArgs mirrors the built-in tools' own no-argument parameter struct, which is
// unexported in that package.
type noArgs struct{}

// streamReply builds an OpenAI-style SSE body from delta objects, one per
// chunk, closed by the [DONE] sentinel.
func streamReply(deltas ...string) string {
	lines := make([]string, 0, len(deltas)*2+2)
	for _, d := range deltas {
		lines = append(lines, `data: {"choices":[{"delta":`+d+`}]}`, ``)
	}
	lines = append(lines, `data: [DONE]`, ``)
	return strings.Join(lines, "\n")
}

// sseServer serves one canned body for every request and counts the calls, so a
// fixture can answer a tool-calling round and the round after it differently.
func sseServer(t *testing.T, body func(call int) string) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body(int(calls.Add(1))))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// einoEngineFor builds the real eino engine against a fixture upstream.
func einoEngineFor(t *testing.T, url string) agentapi.Engine {
	t.Helper()
	eng := NewEngine(EngineConfig{BaseURL: url, Model: "test-model"})
	if eng == nil {
		t.Fatal("NewEngine returned nil for a configured backend")
	}
	return eng
}

// collect splits a drained run into the three things the card cares about.
type runResult struct {
	reasoning string
	answer    string
	final     string
	order     []agentapi.EventType
}

func runEngine(t *testing.T, engine agentapi.Engine, opts agentapi.RunOptions) runResult {
	t.Helper()
	ch, err := engine.Run(context.Background(),
		[]agentapi.Message{{Role: agentapi.RoleUser, Content: "9.11 和 9.9 哪个大"}}, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var out runResult
	for _, ev := range drain(t, ch) {
		out.order = append(out.order, ev.Type)
		switch ev.Type {
		case agentapi.EventReasoning:
			out.reasoning += ev.Delta
		case agentapi.EventAnswer:
			out.answer += ev.Delta
		case agentapi.EventDone:
			out.final += ev.Final
		}
	}
	return out
}

// TestEinoEngineEmitsReasoningEvents is the regression test for the empty
// "深度思考中" (Thinking) panel: before this, the engine read only Content, so an
// upstream that streamed reasoning_content produced no reasoning event at all.
func TestEinoEngineEmitsReasoningEvents(t *testing.T) {
	srv := sseServer(t, func(int) string {
		return streamReply(
			`{"reasoning_content":"先看十分位。"}`,
			`{"reasoning_content":"9 > 1。"}`,
			`{"content":"9.9 更大"}`,
		)
	})

	got := runEngine(t, einoEngineFor(t, srv.URL), agentapi.RunOptions{Reasoning: true, RunID: "reason-on"})

	if want := "先看十分位。9 > 1。"; got.reasoning != want {
		t.Fatalf("reasoning = %q, want %q", got.reasoning, want)
	}
	if want := "9.9 更大"; got.answer != want {
		t.Fatalf("answer = %q, want %q", got.answer, want)
	}
	// Final is the answer the card closes on; reasoning folded into it would be
	// rendered a second time.
	if got.final != got.answer {
		t.Fatalf("Final = %q, want the answer %q", got.final, got.answer)
	}
	if strings.Contains(got.final, "十分位") {
		t.Fatalf("Final carries reasoning text: %q", got.final)
	}

	// Ordering is what lets the live card show thinking before the answer.
	firstReasoning, firstAnswer := -1, -1
	for i, typ := range got.order {
		if typ == agentapi.EventReasoning && firstReasoning < 0 {
			firstReasoning = i
		}
		if typ == agentapi.EventAnswer && firstAnswer < 0 {
			firstAnswer = i
		}
	}
	if firstReasoning < 0 || firstAnswer < 0 {
		t.Fatalf("missing lanes in %v", got.order)
	}
	if firstReasoning > firstAnswer {
		t.Fatalf("reasoning must precede the answer: %v", got.order)
	}
}

// TestEinoEngineSuppressesReasoningWhenNotRequested pins the gate. A client that
// turned reasoning off has no panel to render it into, and an update aimed at a
// missing card element fails the call that also carries the answer text.
func TestEinoEngineSuppressesReasoningWhenNotRequested(t *testing.T) {
	srv := sseServer(t, func(int) string {
		return streamReply(
			`{"reasoning_content":"先看十分位。"}`,
			`{"content":"9.9 更大"}`,
		)
	})

	got := runEngine(t, einoEngineFor(t, srv.URL), agentapi.RunOptions{Reasoning: false, RunID: "reason-off"})

	if got.reasoning != "" {
		t.Fatalf("reasoning leaked while switched off: %q", got.reasoning)
	}
	if want := "9.9 更大"; got.answer != want {
		t.Fatalf("answer = %q, want %q", got.answer, want)
	}
}

// TestEinoEngineToleratesAnswerOnlyUpstream guards the restructured branches
// against an upstream that simply never thinks.
func TestEinoEngineToleratesAnswerOnlyUpstream(t *testing.T) {
	srv := sseServer(t, func(int) string {
		return streamReply(`{"content":"9.9 更大"}`)
	})

	got := runEngine(t, einoEngineFor(t, srv.URL), agentapi.RunOptions{Reasoning: true, RunID: "no-think"})

	if got.reasoning != "" {
		t.Fatalf("invented reasoning from an answer-only upstream: %q", got.reasoning)
	}
	if want := "9.9 更大"; got.answer != want {
		t.Fatalf("answer = %q, want %q", got.answer, want)
	}
}

// TestEinoEngineToolCallRoundKeepsReasoningAndTools is the interaction test:
// reasoning now rides the model stream alongside tool-call deltas, so it has to
// be proven that (a) a reasoning chunk does not derail the react loop's tool-call
// detection, (b) both rounds' reasoning reaches the events, and (c) tool steps
// still arrive.
func TestEinoEngineToolCallRoundKeepsReasoningAndTools(t *testing.T) {
	srv := sseServer(t, func(call int) string {
		if call == 1 {
			return streamReply(
				`{"reasoning_content":"先查时间。"}`,
				`{"tool_calls":[{"index":0,"id":"call_1","type":"function",`+
					`"function":{"name":"current_time","arguments":"{}"}}]}`,
			)
		}
		return streamReply(
			`{"reasoning_content":"拿到时间了。"}`,
			`{"content":"现在是 12:00"}`,
		)
	})

	clock, err := utils.InferTool("current_time",
		"Return the current server time in RFC3339 format.",
		func(_ context.Context, _ noArgs) (string, error) { return "12:00", nil })
	if err != nil {
		t.Fatalf("InferTool: %v", err)
	}

	eng := NewEngine(EngineConfig{
		BaseURL: srv.URL,
		Model:   "test-model",
		Tools:   func([]string, []string) []tool.BaseTool { return []tool.BaseTool{clock} },
		// A tool-bearing run needs a checkpoint store even when nothing is
		// gated: any tool may pause, and a pause with nowhere to be saved cannot
		// be resumed. See TestEinoEngineRefusesToRunWhatItCannotResume.
		Checkpoints: checkpoint.New(nil, "onex", time.Minute),
	})
	if eng == nil {
		t.Fatal("NewEngine returned nil for a configured backend")
	}

	got := runEngine(t, eng, agentapi.RunOptions{Reasoning: true, RunID: "tools"})

	if want := "先查时间。拿到时间了。"; got.reasoning != want {
		t.Fatalf("reasoning across rounds = %q, want %q", got.reasoning, want)
	}
	if want := "现在是 12:00"; got.answer != want {
		t.Fatalf("answer = %q, want %q", got.answer, want)
	}

	var sawToolStep bool
	for _, typ := range got.order {
		if typ == agentapi.EventToolStep || typ == agentapi.EventProgress {
			sawToolStep = true
		}
	}
	if !sawToolStep {
		t.Fatalf("the tool round produced no step: %v", got.order)
	}
}

// TestUnattendedRunDropsAskUserQuestion pins the scheduled-run contract: a run
// nobody is watching must not be offered a tool whose only behavior is to stop
// and wait for a person. The attended run keeps it.
func TestUnattendedRunDropsAskUserQuestion(t *testing.T) {
	clock, err := utils.InferTool("current_time",
		"Return the current server time in RFC3339 format.",
		func(_ context.Context, _ noArgs) (string, error) { return "12:00", nil })
	if err != nil {
		t.Fatalf("InferTool clock: %v", err)
	}
	asking, err := utils.InferTool("ask_user_question", "Ask the user a question.",
		func(_ context.Context, _ noArgs) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("InferTool ask: %v", err)
	}

	eng := &einoEngine{tools: func([]string, []string) []tool.BaseTool {
		return []tool.BaseTool{clock, asking}
	}}

	names := func(spec RunSpec) []string {
		tools := eng.toolsForSpec(context.Background(), spec)
		out := make([]string, 0, len(tools))
		for _, tl := range tools {
			info, err := tl.Info(context.Background())
			if err != nil || info == nil {
				continue
			}
			out = append(out, info.Name)
		}
		return out
	}

	attended := names(RunSpec{})
	if !slices.Contains(attended, "ask_user_question") {
		t.Fatalf("attended run lost ask_user_question: %v", attended)
	}

	unattended := names(RunSpec{Unattended: true})
	if slices.Contains(unattended, "ask_user_question") {
		t.Fatalf("unattended run was offered ask_user_question: %v", unattended)
	}
	if !slices.Contains(unattended, "current_time") {
		t.Fatalf("unattended run lost an ordinary tool: %v", unattended)
	}
}

// TestEinoEngineRefusesToRunWhatItCannotResume closes the quiet failure of a
// run that can pause with nowhere to save the pause.
//
// adk checkpoints only when it has a store: given none, the runner hands the
// iterator straight through, so a tool that interrupts still raises its question
// and still reaches the caller — and the run parks in a state nothing can
// resume, because the answer to it has no checkpoint to be applied to. The human
// is asked either way. Refusing the run says so while an operator can act on it.
//
// ask_user_question is in the built-in tool set of every tool-bearing run, so
// this is the ordinary configuration rather than a corner one.
func TestEinoEngineRefusesToRunWhatItCannotResume(t *testing.T) {
	asking, err := utils.InferTool("ask_user_question", "Ask the user a question.",
		func(ctx context.Context, _ noArgs) (string, error) {
			return "", tool.Interrupt(ctx, &agentapi.AskQuestionInfo{Question: "哪个集群？"})
		})
	if err != nil {
		t.Fatalf("InferTool: %v", err)
	}

	// The upstream is never reached: the run is refused before the agent is
	// assembled. It is here because NewEngine needs a base URL to build any
	// engine at all.
	eng := NewEngine(EngineConfig{
		BaseURL: "http://127.0.0.1:1",
		Model:   "test-model",
		Tools:   func([]string, []string) []tool.BaseTool { return []tool.BaseTool{asking} },
	})
	if eng == nil {
		t.Fatal("NewEngine returned nil for a configured backend")
	}

	ch, err := eng.Run(context.Background(),
		[]agentapi.Message{{Role: agentapi.RoleUser, Content: "删掉集群"}}, agentapi.RunOptions{RunID: "no-store"})
	if err == nil {
		t.Fatalf("a run that can pause started without a checkpoint store: %+v", drain(t, ch))
	}
	if !strings.Contains(err.Error(), "checkpoint store") {
		t.Errorf("the failure %q does not say what is missing", err)
	}
}

// halfReply is the delta an upstream sends before its stream is severed.
const halfReply = `data: {"choices":[{"delta":{"content":"半句"}}]}` + "\n\n"

// TestEinoEngineReportsASeveredModelStream pins what a cut model stream must
// look like from the engine: a failure, never a completed run. The engine's own
// read loop stops at the error without emitting anything, and the runner reports
// it as the node's failure — so the card goes red on a half answer rather than
// closing green on one.
//
// The same severed upstream is what a server-side HTTP write deadline produces
// one hop out, which is why this is worth pinning here and not only in the
// transport.
func TestEinoEngineReportsASeveredModelStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Declaring more than is delivered and then returning is what a severed
		// stream looks like from the client: the body ends short of its declared
		// length, so reading it fails with io.ErrUnexpectedEOF instead of EOF.
		w.Header().Set("Content-Length", "4096")
		_, _ = io.WriteString(w, halfReply)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	ch, err := einoEngineFor(t, srv.URL).Run(context.Background(),
		[]agentapi.Message{{Role: agentapi.RoleUser, Content: "9.11 和 9.9 哪个大"}}, agentapi.RunOptions{RunID: "severed"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var failed *agentapi.Event
	for _, ev := range drain(t, ch) {
		switch ev.Type {
		case agentapi.EventDone:
			t.Fatalf("a severed stream was reported as a completed run: %+v", ev)
		case agentapi.EventFailed:
			e := ev
			failed = &e
		}
	}
	if failed == nil {
		t.Fatal("the run reported neither a failure nor a completion")
	}
	if failed.Err == nil {
		t.Fatal("the failure event carries no cause")
	}
	// The cause is what reaches the card, so it has to survive the runner's
	// wrapping — a failure reported as an opaque "node run error" would send
	// whoever reads the log looking in the wrong place.
	if !errors.Is(failed.Err, io.ErrUnexpectedEOF) {
		t.Fatalf("the read failure was lost on the way out: %v", failed.Err)
	}
}

// TestEinoEngineReportsAStopWhenTheRunIsCancelled pins the other side of it: the
// stop button cancels the run context, which tears the model stream down with the
// same kind of read error. Reporting that as a failure would paint a red card
// over a run the user deliberately stopped.
func TestEinoEngineReportsAStopWhenTheRunIsCancelled(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, halfReply)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hold the stream open so the cancellation lands mid-answer.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := einoEngineFor(t, srv.URL).Run(ctx,
		[]agentapi.Message{{Role: agentapi.RoleUser, Content: "9.11 和 9.9 哪个大"}}, agentapi.RunOptions{RunID: "cancelled"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Cancel once the answer has started arriving, so the stream — not the
	// request setup — is what gets torn down.
	deadline := time.After(3 * time.Second)
	for started := false; !started; {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("the run ended before it streamed anything")
			}
			started = ev.Type == agentapi.EventAnswer
		case <-deadline:
			t.Fatal("timed out waiting for the answer to start")
		}
	}
	cancel()

	for _, ev := range drain(t, ch) {
		switch ev.Type {
		case agentapi.EventFailed:
			t.Fatalf("a cancelled run was reported as a failure: %v", ev.Err)
		case agentapi.EventStopped:
			return
		}
	}
	t.Fatal("a cancelled run reported neither a stop nor a failure")
}

// TestNewEngineRequiresAModel pins the one way to disable the engine: with no
// shared model and no connection fields there is nothing to reason with, and a
// nil engine is how the server reports "no LLM configured".
func TestNewEngineRequiresAModel(t *testing.T) {
	if eng := NewEngine(EngineConfig{}); eng != nil {
		t.Fatalf("NewEngine returned %T without a model", eng)
	}
}

// TestNewEngineAlwaysBuildsEino pins the removal of the http engine: the only
// engine NewEngine can return is the eino one.
func TestNewEngineAlwaysBuildsEino(t *testing.T) {
	eng := NewEngine(EngineConfig{BaseURL: "http://127.0.0.1:1", Model: "test-model"})
	if eng == nil {
		t.Fatal("NewEngine returned nil for a configured backend")
	}
	if name := eng.Name(); name != "eino" {
		t.Fatalf("engine name = %q, want eino", name)
	}
}
