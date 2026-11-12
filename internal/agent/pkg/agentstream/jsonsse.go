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

package agentstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// jsonSSESink renders an agent event stream as newline-delimited JSON SSE
// (text/event-stream) using the AgentStreamEvent proto. It is the sink backing
// the /v1/agent/ask endpoint consumed by the chat to drive a Lark card.
type jsonSSESink struct {
	w         io.Writer
	flusher   interface{ Flush() }
	runID     string
	sendChunk func(*v1.AgentStreamEvent) error
}

// NewJSONSSESink returns a StreamSink that writes AgentStreamEvent JSON to w.
func NewJSONSSESink(w io.Writer, flusher interface{ Flush() }, runID string) StreamSink {
	s := &jsonSSESink{w: w, flusher: flusher, runID: runID}
	s.sendChunk = func(ev *v1.AgentStreamEvent) error {
		ev.RunID = s.runID
		bytes, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("marshal agent stream event: %w", err)
		}
		return writeSSEFrame(s.w, s.flusher, bytes)
	}
	return s
}

func (s *jsonSSESink) Progress(_ context.Context, steps []agent.ToolStep) error {
	return s.sendChunk(&v1.AgentStreamEvent{
		Type:  v1.AgentEventType_agentEventTypeProgress,
		Steps: toProtoSteps(steps),
	})
}

func (s *jsonSSESink) Reasoning(_ context.Context, delta string) error {
	return s.sendChunk(&v1.AgentStreamEvent{
		Type:  v1.AgentEventType_agentEventTypeReasoning,
		Delta: delta,
	})
}

func (s *jsonSSESink) Answer(_ context.Context, delta string) error {
	return s.sendChunk(&v1.AgentStreamEvent{
		Type:  v1.AgentEventType_agentEventTypeAnswer,
		Delta: delta,
	})
}

func (s *jsonSSESink) Interrupted(_ context.Context, it *agent.Interrupt) error {
	ev := &v1.AgentStreamEvent{Type: v1.AgentEventType_agentEventTypeInterrupted}
	if it != nil {
		ev.Interrupt = &v1.AgentInterrupt{
			RequestID: it.RequestID,
			Requests:  toProtoRequests(it.Requests),
		}
	}
	return s.sendChunk(ev)
}

// Image sends the generated references as their own event.
//
// The type is a value older clients do not know, and their translation tables
// map an unknown value to "not an event" — so a client built before images
// existed drops this rather than misreading it as an answer. See the proto's
// comment on the enum value.
func (s *jsonSSESink) Image(_ context.Context, refs []media.Ref) error {
	return s.sendChunk(&v1.AgentStreamEvent{
		Type:   v1.AgentEventType_agentEventTypeImage,
		Images: agent.RefsToProto(refs),
	})
}

// Sources sends the documents a retrieval returned as their own event.
//
// Same shape and same rule as Image: the type is a value older clients do not
// know, and their translation tables map an unknown value to "not an event" —
// so a client built before sources existed drops this rather than misreading it
// as something else.
func (s *jsonSSESink) Sources(_ context.Context, refs []agent.SourceRef) error {
	return s.sendChunk(&v1.AgentStreamEvent{
		Type:    v1.AgentEventType_agentEventTypeSource,
		Sources: agent.SourcesToProto(refs),
	})
}

func (s *jsonSSESink) Usage(_ context.Context, u *agent.Usage) error {
	if u == nil {
		return nil
	}
	return s.sendChunk(&v1.AgentStreamEvent{
		Type:  v1.AgentEventType_agentEventTypeUsage,
		Usage: toProtoUsage(u),
	})
}

func (s *jsonSSESink) Done(_ context.Context, finalAnswer string) error {
	return s.sendChunk(&v1.AgentStreamEvent{
		Type:  v1.AgentEventType_agentEventTypeDone,
		Final: finalAnswer,
	})
}

func (s *jsonSSESink) Failed(_ context.Context, err error) error {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return s.sendChunk(&v1.AgentStreamEvent{
		Type:  v1.AgentEventType_agentEventTypeFailed,
		Error: message,
	})
}

func (s *jsonSSESink) Stopped(_ context.Context) error {
	return s.sendChunk(&v1.AgentStreamEvent{Type: v1.AgentEventType_agentEventTypeStopped})
}

// Suspended ends a stream whose run paused for approval. The pause itself went
// out as an interrupted event, which is what the caller answers; this only says
// the stream is over without the run being.
func (s *jsonSSESink) Suspended(_ context.Context) error {
	return s.sendChunk(&v1.AgentStreamEvent{Type: v1.AgentEventType_agentEventTypeSuspended})
}

// toProtoRequests converts the pending calls of a pause to their proto form.
func toProtoRequests(reqs []agent.InterruptRequest) []*v1.AgentInterruptRequest {
	out := make([]*v1.AgentInterruptRequest, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, &v1.AgentInterruptRequest{
			Address:     r.Address,
			ToolName:    r.ToolName,
			Question:    r.Question,
			Kind:        r.Kind,
			Options:     r.Options,
			MultiSelect: r.MultiSelect,
			Arguments:   r.Arguments,
		})
	}
	return out
}

// toProtoSteps converts backend-neutral tool steps to their proto form.
func toProtoSteps(steps []agent.ToolStep) []*v1.AgentToolStep {
	out := make([]*v1.AgentToolStep, 0, len(steps))
	for _, st := range steps {
		out = append(out, &v1.AgentToolStep{
			Id:         st.ID,
			Name:       st.Name,
			Status:     st.Status,
			DurationMs: st.Duration.Milliseconds(),
			Tokens:     int32(st.Tokens),
		})
	}
	return out
}

// toProtoUsage converts accumulated usage to its proto form.
func toProtoUsage(u *agent.Usage) *v1.AgentUsage {
	if u == nil {
		return nil
	}
	return &v1.AgentUsage{
		PromptTokens:     int32(u.PromptTokens),
		CompletionTokens: int32(u.CompletionTokens),
		TotalTokens:      int32(u.TotalTokens),
		ReasoningTokens:  int32(u.ReasoningTokens),
		Calls:            int32(u.Calls),
		// Carried rather than dropped: the caller is where a run is accounted
		// for, and it cannot price the input without knowing which part of it was
		// cached. Both are 0 for an upstream that reports no split.
		CacheHitTokens:  int32(u.CacheHitTokens),
		CacheMissTokens: int32(u.CacheMissTokens),
	}
}
