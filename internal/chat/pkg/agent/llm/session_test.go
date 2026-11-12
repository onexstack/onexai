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
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
	"github.com/onexstack/onexai/pkg/media"
)

// answerEvent and reasoningEvent build the two delta-carrying stream events.
func answerEvent(delta string) *llmv1.AgentStreamEvent {
	return &llmv1.AgentStreamEvent{Type: llmv1.AgentEventType_agentEventTypeAnswer, Delta: delta}
}

func reasoningEvent(delta string) *llmv1.AgentStreamEvent {
	return &llmv1.AgentStreamEvent{Type: llmv1.AgentEventType_agentEventTypeReasoning, Delta: delta}
}

// fakeClient is the agent side of the port, recording what the adapter asked
// of it. Only the methods the adapter uses are overridden; the embedded interface
// stays nil, so anything else panics loudly instead of passing silently.
type fakeClient struct {
	agentclient.Interface

	reqs      []*llmv1.AgentAskRequest
	events    []*llmv1.AgentStreamEvent
	streamErr error

	approveReqs   []*llmv1.AgentApproveRequest
	approveEvents []*llmv1.AgentStreamEvent
	approveErr    error
	// approveOpen, when set, holds the resumed stream open until the test closes
	// it: the answer is documented to return without waiting for the run, and that
	// claim can only be checked against a run that has not finished.
	approveOpen chan struct{}
	// approveDone is closed when the resumed stream returns, so a test can wait for
	// the adapter's pump instead of sleeping.
	approveDone chan struct{}

	stopped []string
	stopErr error
}

func (f *fakeClient) AgentAskStream(_ context.Context, req *llmv1.AgentAskRequest, onEvent func(*llmv1.AgentStreamEvent) error) error {
	f.reqs = append(f.reqs, req)
	for _, ev := range f.events {
		if err := onEvent(ev); err != nil {
			// The real client returns the handler's error unchanged; mirroring that
			// is the whole point of the abort test below.
			return err
		}
	}
	return f.streamErr
}

func (f *fakeClient) AgentApproveStream(
	_ context.Context, req *llmv1.AgentApproveRequest, onEvent func(*llmv1.AgentStreamEvent) error,
) error {
	f.approveReqs = append(f.approveReqs, req)
	if f.approveDone != nil {
		defer close(f.approveDone)
	}
	if f.approveOpen != nil {
		<-f.approveOpen
	}
	for _, ev := range f.approveEvents {
		if err := onEvent(ev); err != nil {
			return err
		}
	}
	return f.approveErr
}

func (f *fakeClient) Stop(_ context.Context, runID string) error {
	f.stopped = append(f.stopped, runID)
	return f.stopErr
}

// TestAgentAskStreamCarriesEveryRequestField is the guard for a refactor that is
// meant to be invisible: the turn's inputs now travel through the port's request
// struct, and a field dropped in the translation would silently change what the
// model is asked rather than fail anything.
func TestAgentAskStreamCarriesEveryRequestField(t *testing.T) {
	client := &fakeClient{}
	s := &session{client: client, key: "oc_conv"}

	err := s.AskStream(context.Background(), agent.AskRequest{
		Question:  "这个问题",
		Images:    []media.Ref{{URL: "data:image/png;base64,AAAA"}},
		Reasoning: true,
		Agents:    []string{"code"},
		SenderID:  "ou_2f8d0a4c9b7e4f1a",
		RunID:     "run-1",
	}, func(agent.Event) error { return nil })
	if err != nil {
		t.Fatalf("AskStream returned error: %v", err)
	}

	if len(client.reqs) != 1 {
		t.Fatalf("agent saw %d requests, want 1", len(client.reqs))
	}
	got := client.reqs[0]
	if got.GetSessionId() != "oc_conv" {
		t.Errorf("SessionKey = %q, want oc_conv", got.GetSessionId())
	}
	if got.GetInput().GetText() != "这个问题" {
		t.Errorf("Question = %q, want 这个问题", got.GetInput().GetText())
	}
	if len(got.GetInput().GetImages()) != 1 {
		t.Errorf("Images = %+v, want 1 entry", got.GetInput().GetImages())
	}
	if !got.GetGeneration().GetReasoning() {
		t.Error("Reasoning = false, want true")
	}
	if len(got.GetSelection().GetAgents()) != 1 || got.GetSelection().GetAgents()[0] != "code" {
		t.Errorf("Agents = %+v, want [code]", got.GetSelection().GetAgents())
	}
	// The asker's ownership key, not a display name: this is what the agent
	// forwards to MCP tools, where it decides whose job/cronjob rows are in
	// scope. A field dropped here would silently let the model act as nobody.
	if got.GetSubject().GetId() != "ou_2f8d0a4c9b7e4f1a" {
		t.Errorf("SenderID = %q, want ou_2f8d0a4c9b7e4f1a", got.GetSubject().GetId())
	}
	if got.GetRunId() != "run-1" {
		t.Errorf("RunID = %q, want run-1", got.GetRunId())
	}
}

// TestAgentAskStreamBusyMapsToQueued pins the one behaviour this refactor is
// allowed to change. oai-agent-apiserver refuses a turn for a conversation that still
// has a run in flight, and that refusal must reach the caller as ErrBusy — the
// signal to tell the user their message is queued — rather than as the failure
// the transport reported. A generic error here is what turns a busy conversation
// into a red card that reads like a broken agent.
func TestAgentAskStreamBusyMapsToQueued(t *testing.T) {
	t.Run("the clients busy error becomes the ports busy error", func(t *testing.T) {
		// Wrapped, the way the HTTP client builds it: the mapping has to survive
		// the transport adding its own context to the message.
		client := &fakeClient{streamErr: fmt.Errorf("%w (HTTP %d)", agentclient.ErrBusy, 409)}
		s := &session{client: client, key: "oc_conv"}

		err := s.AskStream(context.Background(), agent.AskRequest{Question: "问题"}, func(agent.Event) error { return nil })
		if !errors.Is(err, agent.ErrBusy) {
			t.Fatalf("AskStream error = %v, want agent.ErrBusy", err)
		}
		if !errors.Is(err, agentclient.ErrBusy) {
			t.Errorf("the mapping discarded the transport error, so the cause is no longer inspectable: %v", err)
		}
	})

	t.Run("any other failure is reported as it arrived", func(t *testing.T) {
		client := &fakeClient{streamErr: errors.New("connection reset")}
		s := &session{client: client, key: "oc_conv"}

		err := s.AskStream(context.Background(), agent.AskRequest{Question: "问题"}, func(agent.Event) error { return nil })
		if err == nil {
			t.Fatal("AskStream returned nil, want the transport error")
		}
		if errors.Is(err, agent.ErrBusy) {
			t.Error("an unrelated failure was reported as the conversation being busy")
		}
	})

	t.Run("a caller aborting the stream is not busy", func(t *testing.T) {
		abort := errors.New("the caller stopped listening")
		client := &fakeClient{events: []*llmv1.AgentStreamEvent{answerEvent("x")}}
		s := &session{client: client, key: "oc_conv"}

		err := s.AskStream(context.Background(), agent.AskRequest{Question: "问题"}, func(agent.Event) error { return abort })
		if !errors.Is(err, abort) {
			t.Fatalf("AskStream error = %v, want the handler's own error", err)
		}
		if errors.Is(err, agent.ErrBusy) {
			t.Error("an abort was reported as the conversation being busy")
		}
	})
}

// TestAgentAskStreamForwardsTranslatedEvents pins that the adapter is a
// pass-through: the caller sees the port's events, in order, and unknown ones are
// dropped rather than passed on half-understood.
func TestAgentAskStreamForwardsTranslatedEvents(t *testing.T) {
	client := &fakeClient{events: []*llmv1.AgentStreamEvent{
		{Type: llmv1.AgentEventType_agentEventTypeUnspecified},
		reasoningEvent("想一下"),
		answerEvent("答案"),
		{Type: llmv1.AgentEventType_agentEventTypeDone, Final: "答案"},
	}}
	s := &session{client: client, key: "oc_conv"}

	var got []agent.Event
	err := s.AskStream(context.Background(), agent.AskRequest{Question: "问题"}, func(ev agent.Event) error {
		got = append(got, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("AskStream returned error: %v", err)
	}

	want := []agent.EventType{agent.EventReasoning, agent.EventAnswer, agent.EventDone}
	if len(got) != len(want) {
		t.Fatalf("forwarded %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, typ := range want {
		if got[i].Type != typ {
			t.Errorf("event %d = %d, want %d", i, got[i].Type, typ)
		}
	}
	if got[0].Delta != "想一下" || got[1].Delta != "答案" {
		t.Errorf("deltas lost in the adapter: %+v", got)
	}
	if got[2].Final != "答案" {
		t.Errorf("Final = %q, want 答案", got[2].Final)
	}
}

// TestAnswerPermissionDeliversToThePausedRun pins the port's promise for an
// answer: the exact run and pause are named on the wire, the continuation reaches
// the sink the run already had, and the call returns without waiting for the run
// to finish — a card click cannot be held open for a whole run.
func TestAnswerPermissionDeliversToThePausedRun(t *testing.T) {
	open := make(chan struct{})
	client := &fakeClient{
		approveOpen:   open,
		approveEvents: []*llmv1.AgentStreamEvent{answerEvent("后半段")},
		approveDone:   make(chan struct{}),
	}
	s := &session{client: client, key: "oc_conv"}

	var got []agent.Event
	s.attach("run-1", func(ev agent.Event) error {
		got = append(got, ev)
		return nil
	})

	if err := s.AnswerPermission(context.Background(), agent.AnswerRequest{
		RunID:     "run-1",
		RequestID: "req-1",
		Decision:  agent.DecisionAllowAll,
		Answers:   map[string]string{"addr-a": "选项 B"},
		Reason:    "这里需要说明",
	}); err != nil {
		t.Fatalf("AnswerPermission returned error: %v", err)
	}
	// Returned while the run is still open: the answer is a delivery, not a wait.
	select {
	case <-client.approveDone:
		t.Fatal("AnswerPermission waited for the run to finish")
	default:
	}

	close(open)
	<-client.approveDone

	if len(client.approveReqs) != 1 {
		t.Fatalf("client saw %d answers, want 1", len(client.approveReqs))
	}
	req := client.approveReqs[0]
	if req.GetRunID() != "run-1" || req.GetRequestID() != "req-1" {
		t.Errorf("answer named run %q pause %q, want run-1/req-1", req.GetRunID(), req.GetRequestID())
	}
	if req.GetDecision() != agent.DecisionAllowAll {
		t.Errorf("decision = %q, want %q", req.GetDecision(), agent.DecisionAllowAll)
	}
	if req.GetAnswers()["addr-a"] != "选项 B" {
		t.Errorf("the answer to the question was lost: %+v", req.GetAnswers())
	}
	if req.GetReason() != "这里需要说明" {
		t.Errorf("the stated reason was lost: %q", req.GetReason())
	}
	// The continuation renders where the first half did, or the card would show
	// the second half of a turn as if it were the whole of it.
	if len(got) != 1 || got[0].Type != agent.EventAnswer || got[0].Delta != "后半段" {
		t.Errorf("continuation events = %+v, want one answer delta", got)
	}
}

// TestAnswerPermissionRefusesWhatItCannotAnswer pins the two refusals that are
// about this conversation rather than the backend: there is no run to answer, or
// the answer names a run that is not the one paused here. Both are refused before
// anything is sent, because a delivered answer resumes a run.
func TestAnswerPermissionRefusesWhatItCannotAnswer(t *testing.T) {
	t.Run("no run was ever attached", func(t *testing.T) {
		s := &session{client: &fakeClient{}, key: "oc_conv"}
		if err := s.AnswerPermission(context.Background(), agent.AnswerRequest{RunID: "run-1"}); err == nil {
			t.Fatal("AnswerPermission answered a conversation with no run")
		}
	})

	t.Run("the run is not the one paused here", func(t *testing.T) {
		client := &fakeClient{}
		s := &session{client: client, key: "oc_conv"}
		s.attach("run-1", func(agent.Event) error { return nil })

		if err := s.AnswerPermission(context.Background(), agent.AnswerRequest{RunID: "run-2"}); err == nil {
			t.Fatal("AnswerPermission answered a run this conversation is not on")
		}
		if len(client.approveReqs) != 0 {
			t.Errorf("a refused answer still reached the client: %+v", client.approveReqs)
		}
	})
}

// TestAnswerPermissionReportsLateFailuresToTheSink pins the half-failure case: the
// answer was delivered, so there is no caller left to return an error to, and the
// run's own renderer is the only thing still looking. A sink failure is not
// reported back to the sink that raised it — that would be a loop.
func TestAnswerPermissionReportsLateFailuresToTheSink(t *testing.T) {
	t.Run("a refusal arrives as a failure event", func(t *testing.T) {
		client := &fakeClient{
			approveErr:  fmt.Errorf("%w (HTTP 410)", agentclient.ErrNotPending),
			approveDone: make(chan struct{}),
		}
		s := &session{client: client, key: "oc_conv"}

		failed := make(chan error, 1)
		s.attach("run-1", func(ev agent.Event) error {
			if ev.Type == agent.EventFailed {
				failed <- ev.Err
			}
			return nil
		})

		if err := s.AnswerPermission(context.Background(), agent.AnswerRequest{RunID: "run-1"}); err != nil {
			t.Fatalf("AnswerPermission returned error: %v", err)
		}

		select {
		case err := <-failed:
			// Matchable both ways: the caller decides what to tell the user by the
			// sentinel, and the log still names the refusal that came back.
			if !errors.Is(err, agent.ErrNotPending) {
				t.Errorf("failure = %v, want it to match agent.ErrNotPending", err)
			}
			if !errors.Is(err, agentclient.ErrNotPending) {
				t.Errorf("failure = %v, want the transport error preserved", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the refusal never reached the sink")
		}
	})

	t.Run("a sink that fails is not told about its own failure", func(t *testing.T) {
		sinkErr := errors.New("the card is gone")
		client := &fakeClient{
			approveEvents: []*llmv1.AgentStreamEvent{answerEvent("还在写")},
			approveDone:   make(chan struct{}),
		}
		s := &session{client: client, key: "oc_conv"}

		calls := make(chan struct{}, 8)
		s.attach("run-1", func(agent.Event) error {
			calls <- struct{}{}
			return sinkErr
		})

		if err := s.AnswerPermission(context.Background(), agent.AnswerRequest{RunID: "run-1"}); err != nil {
			t.Fatalf("AnswerPermission returned error: %v", err)
		}
		// Let any second report happen before counting.
		time.Sleep(50 * time.Millisecond)
		if n := len(calls); n != 1 {
			t.Errorf("the sink was called %d times, want 1: its own error came back to it", n)
		}
	})
}

// TestStopDelegates pins that stopping a run reaches the client by the run id the
// caller generated.
func TestStopDelegates(t *testing.T) {
	client := &fakeClient{}
	s := &session{client: client, key: "oc_conv"}

	if err := s.Stop(context.Background(), "run-1"); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if len(client.stopped) != 1 || client.stopped[0] != "run-1" {
		t.Errorf("Stop reached the client as %+v, want [run-1]", client.stopped)
	}

	clientErr := errors.New("no such run")
	client.stopErr = clientErr
	if err := s.Stop(context.Background(), "run-2"); !errors.Is(err, clientErr) {
		t.Errorf("Stop error = %v, want the client's error", err)
	}
}

// TestSessionIDIsEmpty pins the documented boundary: agent owns this
// conversation's history under the session key, so there is no second identifier
// to resume with, and nothing local should start pretending there is.
func TestSessionIDIsEmpty(t *testing.T) {
	s := &session{client: &fakeClient{}, key: "oc_conv"}
	if id := s.ID(); id != "" {
		t.Errorf("ID() = %q, want empty on the llm backend", id)
	}
}

// TestAgentName pins that the backend registers under the name the configuration
// and the session state use. A drift here would make every registry lookup miss.
func TestAgentName(t *testing.T) {
	if got := New(&fakeClient{}).Name(); got != agent.BackendLLM {
		t.Errorf("Name() = %q, want %q", got, agent.BackendLLM)
	}
}

// TestAgentSessionRequiresAClient pins the startup contract: a backend with no
// client fails the turn it is asked to run rather than panicking inside the
// stream, which is what an unguarded nil interface call would do.
func TestAgentSessionRequiresAClient(t *testing.T) {
	a := New(nil)
	if _, err := a.Session(context.Background(), "oc_conv"); err == nil {
		t.Fatal("Session returned no error with no client configured")
	}
}

// TestAgentCloseIsIdempotent pins that shutting the process down is safe to do
// twice, which it is when two code paths both clean up.
func TestAgentCloseIsIdempotent(t *testing.T) {
	a := New(&fakeClient{})
	if err := a.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}
}

// TestAgentAskStreamMapsBackendRefusals pins the translation the card depends
// on: the backend's two refusals reach the caller as the port's own errors, so
// the presentation layer can tell "wrong credentials" from "you may not" from
// "something broke" without having to know what an HTTP status is.
func TestAgentAskStreamMapsBackendRefusals(t *testing.T) {
	cases := []struct {
		name    string
		stream  error
		want    error
		notWant error
	}{
		{
			name:    "forbidden becomes permission denied",
			stream:  &agentclient.HTTPError{StatusCode: http.StatusForbidden, Reason: "PermissionDenied"},
			want:    agent.ErrPermissionDenied,
			notWant: agent.ErrUnauthorized,
		},
		{
			name:    "unauthorized becomes unauthorized",
			stream:  &agentclient.HTTPError{StatusCode: http.StatusUnauthorized, Reason: "Unauthenticated"},
			want:    agent.ErrUnauthorized,
			notWant: agent.ErrPermissionDenied,
		},
		{
			name:    "a server fault stays a server fault",
			stream:  &agentclient.HTTPError{StatusCode: http.StatusInternalServerError},
			notWant: agent.ErrPermissionDenied,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &session{client: &fakeClient{streamErr: tc.stream}, key: "oc_conv"}
			err := s.AskStream(context.Background(), agent.AskRequest{Question: "问题"}, func(agent.Event) error { return nil })
			if tc.want == nil {
				if errors.Is(err, agent.ErrPermissionDenied) || errors.Is(err, agent.ErrUnauthorized) {
					t.Fatalf("a server fault was reported as a refusal: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("AskStream error = %v, want %v", err, tc.want)
			}
			if tc.notWant != nil && errors.Is(err, tc.notWant) {
				t.Errorf("the refusal matched the wrong sentinel: %v", err)
			}
			// The transport error has to stay inspectable: the log is the one
			// place the server's own explanation is read.
			var httpErr *agentclient.HTTPError
			if !errors.As(err, &httpErr) {
				t.Errorf("the mapping discarded the transport error: %v", err)
			}
		})
	}
}

// TestMapApproveError pins that resuming a paused run is refused for the same
// reasons the first request was, and translated the same way: the gate runs
// again on the way in.
func TestMapApproveError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{
			name: "gone stays not pending",
			in:   fmt.Errorf("%w: %w", agentclient.ErrNotPending, &agentclient.HTTPError{StatusCode: http.StatusGone}),
			want: agent.ErrNotPending,
		},
		{
			name: "conflict stays busy",
			in:   fmt.Errorf("%w: %w", agentclient.ErrBusy, &agentclient.HTTPError{StatusCode: http.StatusConflict}),
			want: agent.ErrBusy,
		},
		{
			name: "forbidden becomes permission denied",
			in:   &agentclient.HTTPError{StatusCode: http.StatusForbidden},
			want: agent.ErrPermissionDenied,
		},
		{
			name: "unauthorized becomes unauthorized",
			in:   &agentclient.HTTPError{StatusCode: http.StatusUnauthorized},
			want: agent.ErrUnauthorized,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapApproveError(tc.in); !errors.Is(got, tc.want) {
				t.Errorf("mapApproveError = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("anything else is passed through", func(t *testing.T) {
		other := errors.New("connection reset")
		if got := mapApproveError(other); !errors.Is(got, other) {
			t.Errorf("mapApproveError = %v, want the error unchanged", got)
		}
	})
}
