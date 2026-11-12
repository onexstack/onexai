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
	"time"

	"github.com/onexstack/onexai/internal/chat/pkg/agent"
	llmv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// TranslateEvent converts one oai-agent-apiserver stream event into the neutral event
// the chat renders. ok is false for an event that carries nothing this
// port models, such as the unspecified type.
func TranslateEvent(ev *llmv1.AgentStreamEvent) (agent.Event, bool) {
	typ, ok := TranslateEventType(ev.GetType())
	if !ok {
		return agent.Event{}, false
	}

	out := agent.Event{Type: typ}
	switch typ {
	case agent.EventReasoning, agent.EventAnswer:
		out.Delta = ev.GetDelta()
	case agent.EventProgress, agent.EventToolStep:
		out.Steps = translateSteps(ev.GetSteps())
	case agent.EventImage:
		out.Images = translateImageRefs(ev.GetImages())
	case agent.EventDone:
		out.Final = ev.GetFinal()
		// The done event carries the run's authoritative total as well as
		// stating the answer, and it is the one event every stream ends with, so
		// a caller that missed an earlier usage event still ends up with the
		// right figure.
		out.Usage = translateUsage(ev.GetUsage())
	case agent.EventUsage:
		out.Usage = translateUsage(ev.GetUsage())
	case agent.EventFailed:
		// The port carries an error rather than a string, so a caller can wrap it
		// without losing the message. An event with no message still has to be an
		// error, or the failure would read as an empty success.
		message := ev.GetError()
		if message == "" {
			message = "agent run failed"
		}
		out.Err = errors.New(message)
	case agent.EventInterrupted:
		out.Interrupt = translateInterrupt(ev.GetInterrupt())
	case agent.EventStopped:
		// Nothing to carry: the type is the whole message.
	}
	return out, true
}

// TranslateEventType maps a proto event type onto the port's. It reports
// ok=false for anything it does not model, so a type added to the proto later is
// ignored rather than mistaken for one of these.
func TranslateEventType(t llmv1.AgentEventType) (agent.EventType, bool) {
	switch t {
	case llmv1.AgentEventType_agentEventTypeProgress:
		return agent.EventProgress, true
	case llmv1.AgentEventType_agentEventTypeReasoning:
		return agent.EventReasoning, true
	case llmv1.AgentEventType_agentEventTypeAnswer:
		return agent.EventAnswer, true
	case llmv1.AgentEventType_agentEventTypeToolStep:
		return agent.EventToolStep, true
	case llmv1.AgentEventType_agentEventTypeDone:
		return agent.EventDone, true
	case llmv1.AgentEventType_agentEventTypeFailed:
		return agent.EventFailed, true
	case llmv1.AgentEventType_agentEventTypeStopped:
		return agent.EventStopped, true
	case llmv1.AgentEventType_agentEventTypeInterrupted:
		return agent.EventInterrupted, true
	case llmv1.AgentEventType_agentEventTypeUsage:
		return agent.EventUsage, true
	case llmv1.AgentEventType_agentEventTypeSuspended:
		return agent.EventSuspended, true
	case llmv1.AgentEventType_agentEventTypeImage:
		return agent.EventImage, true
	default:
		return 0, false
	}
}

// translateImageRefs copies the proto image references.
//
// It is the same field mapping the other direction of this contract uses, and
// it is spelled out rather than shared because the proto package cannot import
// this one and this one already imports the proto.
func translateImageRefs(proto []*llmv1.ImageRef) []media.Ref {
	if len(proto) == 0 {
		return nil
	}
	out := make([]media.Ref, 0, len(proto))
	for _, r := range proto {
		if r == nil {
			continue
		}
		out = append(out, media.Ref{
			AssetID:   r.GetAssetId(),
			ObjectKey: r.GetObjectKey(),
			URL:       r.GetUrl(),
			MimeType:  r.GetMimeType(),
			Size:      r.GetSize(),
			Width:     int(r.GetWidth()),
			Height:    int(r.GetHeight()),
			Name:      r.GetName(),
			Source:    r.GetSource(),
		})
	}
	return out
}

// translateSteps copies the proto tool steps. Nil entries are skipped rather
// than passed on: the rich card walks this list, and a nil element would be a
// panic in the middle of rendering an otherwise fine run.
func translateSteps(proto []*llmv1.AgentToolStep) []agent.ToolStep {
	if len(proto) == 0 {
		return nil
	}
	steps := make([]agent.ToolStep, 0, len(proto))
	for _, st := range proto {
		if st == nil {
			continue
		}
		steps = append(steps, agent.ToolStep{
			ID:       st.GetId(),
			Name:     st.GetName(),
			Status:   st.GetStatus(),
			Duration: time.Duration(st.GetDurationMs()) * time.Millisecond,
			Tokens:   int(st.GetTokens()),
			Result:   st.GetResult(),
		})
	}
	return steps
}

// translateUsage copies the accumulated token usage. A usage event without a
// body is reported as nil rather than as a zeroed total, so a renderer that
// omits an unreported figure is not made to print 0.
func translateUsage(u *llmv1.AgentUsage) *agent.Usage {
	if u == nil {
		return nil
	}
	return &agent.Usage{
		Calls:            int(u.GetCalls()),
		PromptTokens:     int(u.GetPromptTokens()),
		CompletionTokens: int(u.GetCompletionTokens()),
		TotalTokens:      int(u.GetTotalTokens()),
		ReasoningTokens:  int(u.GetReasoningTokens()),
	}
}

// translateInterrupt copies an approval pause, one request per interrupt point.
// A request with no kind is a permission prompt: that is the common case, and
// the one whose card offers allow/deny rather than options.
//
// A pause that names nothing is reported as no pause at all. There is then
// nothing to show and nothing to answer — an address is what an answer targets,
// and one invented here would target nothing — so a renderer that sees a nil
// interrupt must treat it as a pause it cannot put to anyone rather than as a
// run that finished.
func translateInterrupt(it *llmv1.AgentInterrupt) *agent.Interrupt {
	if it == nil || len(it.GetRequests()) == 0 {
		return nil
	}
	out := &agent.Interrupt{RequestID: it.GetRequestID()}
	for _, r := range it.GetRequests() {
		if r == nil {
			continue
		}
		kind := r.GetKind()
		if kind == "" {
			kind = agent.InterruptKindPermission
		}
		out.Requests = append(out.Requests, agent.InterruptRequest{
			Address:     r.GetAddress(),
			ToolName:    r.GetToolName(),
			Question:    r.GetQuestion(),
			Kind:        kind,
			Options:     r.GetOptions(),
			MultiSelect: r.GetMultiSelect(),
			Arguments:   r.GetArguments(),
		})
	}
	if len(out.Requests) == 0 {
		return nil
	}
	return out
}
