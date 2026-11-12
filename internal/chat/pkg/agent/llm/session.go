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
	"sync"
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentclient "github.com/onexstack/onexai/pkg/clientset/agent"
)

// answerStreamTimeout bounds the stream an answer opens. The stream is read on a
// goroutine of its own, off the callback that started it, so nothing else is
// left to bound it: a run that never reaches an ending would otherwise leave
// that goroutine reading a socket until the process exits. It only has to exceed
// the server's own run timeout, which is what actually ends such a run.
const answerStreamTimeout = 15 * time.Minute

// session is one conversation on the agent backend. It carries no
// conversation state to keep in step — the key addresses the conversation and
// everything about it is on the other side — but it does hold where the current
// run's events go, because a run outlives the call that started it: a pause ends
// that call, and the answer that continues the run arrives as a later, unrelated
// request.
type session struct {
	client agentclient.Interface
	key    string

	mu sync.Mutex
	// sink is the event sink the current run was given by AskStream, and runID
	// the caller's name for that run. They are read together when the run is
	// answered: the continuation has to reach the same renderer the first half
	// did, or the card would show the second half of a turn as if it were the
	// whole of it.
	sink  func(agent.Event) error
	runID string
}

var _ agent.Session = (*session)(nil)

// ID implements agent.Session. It is empty on purpose — agent addresses this
// conversation by session key and keeps its own memory, so there is no second
// identifier to resume with. pkg/session's State documents the same boundary
// from the other end.
func (s *session) ID() string { return "" }

// AskStream implements agent.Session.
func (s *session) AskStream(ctx context.Context, req agent.AskRequest, onEvent func(agent.Event) error) error {
	// The preference is sent explicitly in both directions rather than left
	// unset for the server's default to fill in. This conversation always has an
	// opinion — reasoning is on unless someone turned it off — and the backend's
	// default is the operator's, not this conversation's: relying on it would let
	// a server-side setting silently contradict what the card on screen was built
	// to show.
	reasoning := req.Reasoning
	protoReq := &llmv1.AgentAskRequest{
		SessionId: s.key,
		Input: &llmv1.Input{
			Text:   req.Question,
			Images: agentapi.RefsToProto(req.Images),
		},
		Generation: &llmv1.GenerationConfig{Reasoning: &reasoning},
		Selection: &llmv1.Selection{
			Agents:        req.Agents,
			InvokedSkills: req.InvokedSkills,
		},
		Subject: &llmv1.Principal{Id: req.SenderID},
		RunId:   req.RunID,
		// The chat surface is metered by the runtime: nobody else writes this
		// run's ledger rows.
		Metering: &llmv1.Metering{Mode: llmv1.Metering_MODE_RUNTIME},
		// The chat can show an approval card, so a gated tool may pause.
		Options: &llmv1.ExecutionOptions{
			Approval: &llmv1.Approval{Channel: llmv1.Approval_CHANNEL_INTERACTIVE},
		},
	}

	// Recorded before the stream starts, not after it ends: the run can pause
	// part-way through, and a pause is exactly when this has to be readable — the
	// answer that continues the run comes in on a later request with nothing else
	// to say where its events belong.
	s.attach(req.RunID, onEvent)

	err := s.client.AgentAskStream(ctx, protoReq, func(ev *llmv1.AgentStreamEvent) error {
		translated, ok := TranslateEvent(ev)
		if !ok {
			return nil
		}
		return onEvent(translated)
	})
	// An error from onEvent travels back through the client and lands here
	// unchanged, which is what lets a caller abort the stream by returning from
	// its own handler. Only the backend's own refusals are translated.
	//
	// Wrapped, not replaced: the caller decides what to do by matching the
	// sentinel, but whoever reads the log next still needs to know which HTTP
	// status came back from agent and why. Both errors stay matchable.
	switch {
	case errors.Is(err, agentclient.ErrBusy):
		return fmt.Errorf("%w: %w", agent.ErrBusy, err)
	case agentclient.IsUnauthenticated(err):
		return fmt.Errorf("%w: %w", agent.ErrUnauthorized, err)
	case agentclient.IsForbidden(err):
		return fmt.Errorf("%w: %w", agent.ErrPermissionDenied, err)
	case agentclient.IsTooManyRequests(err):
		// An allowance refusal, and unlike the permission ones its text is the
		// card: whoever wrote it knows which ceiling was reached, and the person
		// cannot act on "too many requests" alone.
		return fmt.Errorf("%w: %w", agent.ErrQuotaExceeded, err)
	}
	return err
}

// AnswerPermission implements agent.Session.
func (s *session) AnswerPermission(ctx context.Context, req agent.AnswerRequest) error {
	sink, runID := s.attached()
	if sink == nil {
		return errors.New("llm agent: this conversation has no run to answer")
	}
	if req.RunID != "" && runID != "" && req.RunID != runID {
		return fmt.Errorf("llm agent: run %q is not the one paused here (it is %q)", req.RunID, runID)
	}

	protoReq := &llmv1.AgentApproveRequest{
		RunID:     runID,
		RequestID: req.RequestID,
		Decision:  req.Decision,
		Answers:   req.Answers,
		Reason:    req.Reason,
	}
	// The stream is opened off this goroutine, and detached from this context.
	// The caller is a card click with a budget of seconds, while the run it just
	// continued may take minutes; a stream bound to that click's context would be
	// cut off the moment the click was answered. Its failures have nowhere else to
	// go either, so they are reported to the sink instead — see pump.
	go s.pump(context.WithoutCancel(ctx), protoReq, sink)
	return nil
}

// attach records where the run's events go. It overwrites whatever was there:
// the caller serialises turns per conversation, so a new run is the newer one,
// and keeping the older sink would send a later answer to a card that has been
// superseded.
func (s *session) attach(runID string, sink func(agent.Event) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runID, s.sink = runID, sink
}

// attached returns the sink and run id of the run this conversation last
// started.
func (s *session) attached() (func(agent.Event) error, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sink, s.runID
}

// pump drives a resumed run's events into the sink its first half was given.
//
// It runs on its own goroutine because the answer returns as soon as it has been
// sent: the human is looking at a card, and a card click that waited for a whole
// run to finish would be dropped by the platform long before the run did.
func (s *session) pump(ctx context.Context, req *llmv1.AgentApproveRequest, sink func(agent.Event) error) {
	ctx, cancel := context.WithTimeout(ctx, answerStreamTimeout)
	defer cancel()

	// A sink that fails comes back out of the stream as this call's error, which
	// would otherwise be reported straight back to the sink that raised it — a
	// loop, not a report. The sink is the thing that already knows.
	var sinkErr error
	err := s.client.AgentApproveStream(ctx, req, func(ev *llmv1.AgentStreamEvent) error {
		translated, ok := TranslateEvent(ev)
		if !ok {
			return nil
		}
		sinkErr = sink(translated)
		return sinkErr
	})
	if err == nil || sinkErr != nil {
		return
	}
	// There is no caller left to return this to — the click was answered when the
	// answer was sent — so the run's own renderer is the only thing still looking.
	// Reporting it there is what turns "the answer went nowhere" into a card that
	// says so, rather than one left waiting for a run that is not coming.
	_ = sink(agent.Event{Type: agent.EventFailed, Err: mapApproveError(err)})
}

// mapApproveError translates the backend's refusals of an answer into the port's.
//
// Wrapped, not replaced, for the same reason AskStream wraps ErrBusy: a caller
// that has to tell "someone got there first" from "this pause has expired" from
// "something broke" matches the sentinels, and whoever reads the log next still
// needs to know which HTTP status came back. Both errors stay matchable.
//
// The credentials and authorization refusals are translated here too, and not
// only on the ask path: resuming a paused run re-runs the same gate, so an
// approval can be refused for exactly the reasons the first request was.
func mapApproveError(err error) error {
	switch {
	case errors.Is(err, agentclient.ErrNotPending):
		return fmt.Errorf("%w: %w", agent.ErrNotPending, err)
	case errors.Is(err, agentclient.ErrBusy):
		return fmt.Errorf("%w: %w", agent.ErrBusy, err)
	case agentclient.IsUnauthenticated(err):
		return fmt.Errorf("%w: %w", agent.ErrUnauthorized, err)
	case agentclient.IsForbidden(err):
		return fmt.Errorf("%w: %w", agent.ErrPermissionDenied, err)
	default:
		return err
	}
}

// Stop implements agent.Session.
func (s *session) Stop(ctx context.Context, runID string) error {
	return s.client.Stop(ctx, runID)
}

// Close implements agent.Session. The conversation outlives it: agent keeps
// the history until something clears it, and a later turn on the same key simply
// continues where this left off.
func (s *session) Close() error { return nil }
