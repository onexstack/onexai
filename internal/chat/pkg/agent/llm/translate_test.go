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

package llm

import (
	"errors"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// TestTranslateEventKinds pins the whole proto-to-port mapping in one table, so
// a type added to the proto is a visible gap here rather than a silently dropped
// event at runtime. Unknown types are dropped on purpose: rendering half of an
// event this build does not understand would be worse than rendering nothing.
func TestTranslateEventKinds(t *testing.T) {
	cases := []struct {
		name   string
		proto  llmv1.AgentEventType
		want   agent.EventType
		wantOK bool
	}{
		{"progress", llmv1.AgentEventType_agentEventTypeProgress, agent.EventProgress, true},
		{"reasoning", llmv1.AgentEventType_agentEventTypeReasoning, agent.EventReasoning, true},
		{"answer", llmv1.AgentEventType_agentEventTypeAnswer, agent.EventAnswer, true},
		{"tool step", llmv1.AgentEventType_agentEventTypeToolStep, agent.EventToolStep, true},
		{"done", llmv1.AgentEventType_agentEventTypeDone, agent.EventDone, true},
		{"failed", llmv1.AgentEventType_agentEventTypeFailed, agent.EventFailed, true},
		{"stopped", llmv1.AgentEventType_agentEventTypeStopped, agent.EventStopped, true},
		{"interrupted", llmv1.AgentEventType_agentEventTypeInterrupted, agent.EventInterrupted, true},
		{"usage", llmv1.AgentEventType_agentEventTypeUsage, agent.EventUsage, true},
		{"unspecified", llmv1.AgentEventType_agentEventTypeUnspecified, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := TranslateEventType(tc.proto)
			if ok != tc.wantOK {
				t.Fatalf("TranslateEventType(%v) ok = %v, want %v", tc.proto, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("TranslateEventType(%v) = %d, want %d", tc.proto, got, tc.want)
			}
		})
	}

	// The event-level entry point agrees with the type-level one, so a caller
	// that only has the event does not lose the drop signal.
	if _, ok := TranslateEvent(&llmv1.AgentStreamEvent{Type: llmv1.AgentEventType_agentEventTypeUnspecified}); ok {
		t.Error("TranslateEvent accepted an unspecified event, want it dropped")
	}
}

// TestTranslateEventFields covers what each event carries, not just which kind it
// is: a translation that mapped every type correctly and then read the wrong
// field would pass the table above and still lose the answer text.
func TestTranslateEventFields(t *testing.T) {
	t.Run("reasoning and answer carry their delta", func(t *testing.T) {
		for _, typ := range []llmv1.AgentEventType{
			llmv1.AgentEventType_agentEventTypeReasoning,
			llmv1.AgentEventType_agentEventTypeAnswer,
		} {
			got, ok := TranslateEvent(&llmv1.AgentStreamEvent{Type: typ, Delta: "思考中"})
			if !ok {
				t.Fatalf("TranslateEvent(%v) dropped the event", typ)
			}
			if got.Delta != "思考中" {
				t.Errorf("TranslateEvent(%v).Delta = %q, want 思考中", typ, got.Delta)
			}
		}
	})

	t.Run("tool steps keep their id, duration and tokens", func(t *testing.T) {
		got, ok := TranslateEvent(&llmv1.AgentStreamEvent{
			Type: llmv1.AgentEventType_agentEventTypeToolStep,
			Steps: []*llmv1.AgentToolStep{nil, {
				Id: "call_a", Name: "grep", Status: "ok", DurationMs: 1234, Tokens: 820,
			}, nil},
		})
		if !ok {
			t.Fatal("TranslateEvent dropped the event")
		}
		if len(got.Steps) != 1 {
			t.Fatalf("Steps = %+v, want 1 entry", got.Steps)
		}
		// A dropped field here is invisible until the card renders a row without
		// its duration or tokens, so all of them are pinned.
		want := agent.ToolStep{
			ID: "call_a", Name: "grep", Status: "ok", Duration: 1234 * time.Millisecond, Tokens: 820,
		}
		if got.Steps[0] != want {
			t.Errorf("Steps[0] = %+v, want %+v", got.Steps[0], want)
		}
	})

	t.Run("a tool step carries what the tool returned", func(t *testing.T) {
		// The output is the one part of a tool step that cannot be reconstructed
		// from anything else on the card, so a translation that dropped it would
		// leave a panel of tool names and durations with no account of what any of
		// them did.
		const body = `{"triggered":true,"task_id":39}`
		got, ok := TranslateEvent(&llmv1.AgentStreamEvent{
			Type: llmv1.AgentEventType_agentEventTypeToolStep,
			Steps: []*llmv1.AgentToolStep{
				{Id: "call_a", Name: "pipeline", Status: "ok", Result: body},
			},
		})
		if !ok {
			t.Fatal("TranslateEvent dropped the event")
		}
		if got.Steps[0].Result != body {
			t.Errorf("Result = %q, want %q", got.Steps[0].Result, body)
		}
	})

	t.Run("done carries the final text", func(t *testing.T) {
		got, _ := TranslateEvent(&llmv1.AgentStreamEvent{
			Type:  llmv1.AgentEventType_agentEventTypeDone,
			Final: "整段答案",
		})
		if got.Final != "整段答案" {
			t.Errorf("Final = %q, want 整段答案", got.Final)
		}
	})

	t.Run("usage and done both carry the totals", func(t *testing.T) {
		u := &llmv1.AgentUsage{
			PromptTokens: 500, CompletionTokens: 50, TotalTokens: 550, ReasoningTokens: 7, Calls: 2,
		}
		for _, typ := range []llmv1.AgentEventType{
			llmv1.AgentEventType_agentEventTypeUsage,
			llmv1.AgentEventType_agentEventTypeDone,
		} {
			got, ok := TranslateEvent(&llmv1.AgentStreamEvent{Type: typ, Usage: u})
			if !ok {
				t.Fatalf("TranslateEvent(%v) dropped the event", typ)
			}
			want := agent.Usage{
				Calls: 2, PromptTokens: 500, CompletionTokens: 50, TotalTokens: 550, ReasoningTokens: 7,
			}
			if !ptr.EqualTo(got.Usage, want) {
				t.Errorf("TranslateEvent(%v).Usage = %+v, want %+v", typ, got.Usage, want)
			}
		}
	})

	t.Run("usage that was not reported stays nil", func(t *testing.T) {
		// An upstream that reports no usage must leave the field unset rather
		// than zeroed, so the renderer omits the figure instead of printing 0.
		got, _ := TranslateEvent(&llmv1.AgentStreamEvent{
			Type: llmv1.AgentEventType_agentEventTypeDone, Final: "答案",
		})
		if got.Usage != nil {
			t.Errorf("Usage = %+v, want nil when the run reported none", got.Usage)
		}
	})

	t.Run("failed carries an error", func(t *testing.T) {
		got, ok := TranslateEvent(&llmv1.AgentStreamEvent{
			Type:  llmv1.AgentEventType_agentEventTypeFailed,
			Error: "模型超时",
		})
		if !ok {
			t.Fatal("TranslateEvent dropped the failure")
		}
		if got.Err == nil || got.Err.Error() != "模型超时" {
			t.Errorf("Err = %v, want 模型超时", got.Err)
		}
	})

	t.Run("a failure with no message is still a failure", func(t *testing.T) {
		got, _ := TranslateEvent(&llmv1.AgentStreamEvent{Type: llmv1.AgentEventType_agentEventTypeFailed})
		if got.Err == nil {
			t.Fatal("Err = nil, want a placeholder error — a message-less failure must not read as an empty success")
		}
	})

	t.Run("suspended is a terminal event of its own", func(t *testing.T) {
		// A stream that ended because the run paused must not read as an ending:
		// a caller that took it for one would finalize the card the human is
		// still deciding about.
		got, ok := TranslateEvent(&llmv1.AgentStreamEvent{Type: llmv1.AgentEventType_agentEventTypeSuspended})
		if !ok {
			t.Fatal("TranslateEvent dropped the suspension")
		}
		if got.Type != agent.EventSuspended {
			t.Errorf("Type = %v, want EventSuspended", got.Type)
		}
		if got.Err != nil || got.Final != "" {
			t.Errorf("event = %+v, want nothing carried — the type is the whole message", got)
		}
	})

	t.Run("a request without a kind is a permission prompt", func(t *testing.T) {
		got, _ := TranslateEvent(&llmv1.AgentStreamEvent{
			Type: llmv1.AgentEventType_agentEventTypeInterrupted,
			Interrupt: &llmv1.AgentInterrupt{
				RequestID: "req-1",
				Requests: []*llmv1.AgentInterruptRequest{
					{Address: "tool:call-1", ToolName: "Bash", Question: "允许执行 rm 吗？"},
				},
			},
		})
		if got.Interrupt == nil {
			t.Fatal("Interrupt = nil")
		}
		if got.Interrupt.RequestID != "req-1" {
			t.Errorf("RequestID = %q, want req-1 — an answer names the pause it answers", got.Interrupt.RequestID)
		}
		reqs := got.Interrupt.Requests
		if len(reqs) != 1 {
			t.Fatalf("Requests = %+v, want one", reqs)
		}
		if reqs[0].Kind != agent.InterruptKindPermission {
			t.Errorf("Kind = %q, want %q", reqs[0].Kind, agent.InterruptKindPermission)
		}
		if reqs[0].Address != "tool:call-1" || reqs[0].ToolName != "Bash" || reqs[0].Question != "允许执行 rm 吗？" {
			t.Errorf("Requests[0] = %+v, want the address, tool name and question carried through", reqs[0])
		}
	})

	t.Run("a question request carries its options", func(t *testing.T) {
		got, _ := TranslateEvent(&llmv1.AgentStreamEvent{
			Type: llmv1.AgentEventType_agentEventTypeInterrupted,
			Interrupt: &llmv1.AgentInterrupt{
				RequestID: "req-2",
				Requests: []*llmv1.AgentInterruptRequest{{
					Address:     "ask:1",
					Kind:        agent.InterruptKindQuestion,
					Question:    "选哪个？",
					Options:     []string{"a", "b"},
					MultiSelect: true,
				}},
			},
		})
		q, ok := got.Interrupt.FirstQuestion()
		if !ok {
			t.Fatalf("FirstQuestion found none in %+v", got.Interrupt)
		}
		if q.Kind != agent.InterruptKindQuestion {
			t.Errorf("Kind = %q, want %q", q.Kind, agent.InterruptKindQuestion)
		}
		if q.Address != "ask:1" || len(q.Options) != 2 || !q.MultiSelect {
			t.Errorf("question = %+v, want the address, 2 options and multi-select", q)
		}
	})

	t.Run("an approval carries the arguments it would run with", func(t *testing.T) {
		// The arguments are what make the question answerable: "may I use
		// pipeline?" is not a decision anyone can make, and the digest the card
		// draws is built from this field and nothing else.
		const args = `{"env":"edu-onex","service":"content"}`
		got, _ := TranslateEvent(&llmv1.AgentStreamEvent{
			Type: llmv1.AgentEventType_agentEventTypeInterrupted,
			Interrupt: &llmv1.AgentInterrupt{
				RequestID: "req-args",
				Requests: []*llmv1.AgentInterruptRequest{
					{Address: "tool:call-1", ToolName: "pipeline", Arguments: args},
				},
			},
		})
		if n := len(got.Interrupt.Requests); n != 1 {
			t.Fatalf("Requests = %d, want 1", n)
		}
		if got.Interrupt.Requests[0].Arguments != args {
			t.Errorf("Arguments = %q, want %q", got.Interrupt.Requests[0].Arguments, args)
		}
	})

	t.Run("a group of requests is carried whole", func(t *testing.T) {
		// One assistant message can call several gated tools, and the framework
		// reports them as one pause. Dropping any of them would put a card on
		// screen that asks for less than the run is waiting on.
		got, _ := TranslateEvent(&llmv1.AgentStreamEvent{
			Type: llmv1.AgentEventType_agentEventTypeInterrupted,
			Interrupt: &llmv1.AgentInterrupt{
				RequestID: "req-3",
				Requests: []*llmv1.AgentInterruptRequest{
					{Address: "tool:1", ToolName: "delete_job"},
					{Address: "tool:2", ToolName: "deploy"},
					{Address: "ask:1", Kind: agent.InterruptKindQuestion, Question: "选哪个？"},
				},
			},
		})
		if n := len(got.Interrupt.Requests); n != 3 {
			t.Fatalf("Requests = %d, want 3", n)
		}
		if n := len(got.Interrupt.Permissions()); n != 2 {
			t.Errorf("Permissions() = %d, want the two approvals and not the question", n)
		}
	})

	t.Run("an interrupted event with no payload has nothing to present", func(t *testing.T) {
		got, _ := TranslateEvent(&llmv1.AgentStreamEvent{Type: llmv1.AgentEventType_agentEventTypeInterrupted})
		if got.Interrupt != nil {
			t.Errorf("Interrupt = %+v, want nil: there is no pause to put on a card", got.Interrupt)
		}
	})
}

// TestTranslateEventErrorIsNotThePortsBusyError keeps the two error concepts
// apart: a failed run and a refused request are different things, and only the
// second one tells the caller to try again later.
func TestTranslateEventErrorIsNotThePortsBusyError(t *testing.T) {
	got, _ := TranslateEvent(&llmv1.AgentStreamEvent{
		Type:  llmv1.AgentEventType_agentEventTypeFailed,
		Error: agent.ErrBusy.Error(),
	})
	if errors.Is(got.Err, agent.ErrBusy) {
		t.Error("a run failure whose text merely matches the busy message was reported as busy")
	}
}
