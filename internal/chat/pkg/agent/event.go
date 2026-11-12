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
	"time"

	"github.com/onexstack/onexai/pkg/media"
)

// EventType enumerates the kinds of events a run emits.
//
// The names and the shape of Event mirror pkg/agentapi, which is
// the same model on the other side of the wire. That is deliberate repetition,
// not a missing abstraction: the two services are joined by a protobuf stream,
// not by Go types, so sharing the struct would couple two binaries that are
// meant to be deployable apart. Keeping the names identical makes the
// translation in pkg/agent/llm a mapping a reader can check by eye.
type EventType int

const (
	// EventProgress carries the current tool-execution log (Steps).
	EventProgress EventType = iota
	// EventReasoning carries an incremental chunk of reasoning text (Delta).
	EventReasoning
	// EventAnswer carries an incremental chunk of the answer (Delta).
	EventAnswer
	// EventToolStep carries a single updated tool step.
	EventToolStep
	// EventDone is the terminal success event; Final holds the full answer.
	EventDone
	// EventFailed is a terminal error event; Err holds the cause.
	EventFailed
	// EventStopped is a terminal event emitted when the user stopped the run.
	EventStopped
	// EventInterrupted signals the run paused awaiting a human decision.
	EventInterrupted
	// EventUsage carries the run's accumulated token usage so far (Usage).
	EventUsage
	// EventImage carries generated image references (Images). It is not
	// terminal: a run may produce several and go on working.
	EventImage
	// EventSuspended is the terminal event of a stream that ended because the run
	// paused for a decision. It is distinct from Done/Failed/Stopped because the
	// run has not finished: the work so far is not a result, and the run
	// continues when someone answers. A renderer that treated the closed stream
	// as an ending would finalize a card that is still waiting on an answer.
	//
	// A backend whose pause leaves its stream running — the CLI, whose process
	// stays alive holding the request — has no stream to end and so never emits
	// this. A renderer must therefore read a pause from EventInterrupted and not
	// require this event to follow it; what this event adds, where it exists, is
	// that the stream is over.
	EventSuspended
)

// ToolStep describes one tool invocation in the progress log.
type ToolStep struct {
	// ID is the model's tool-call id. It is stable across an approval pause and
	// resume, so a renderer updates a row in place rather than appending a
	// duplicate, and it is how a later event carries this step's tokens.
	ID string
	// Name is the tool's name as the agent reports it.
	Name string
	// Status is "pending", "ok" or "fail".
	Status string
	// Duration is how long the invocation took, as measured where it ran.
	Duration time.Duration
	// Tokens is how many prompt tokens the invocation's result added to the
	// context. The backend derives it from the model calls around the tool rather
	// than measuring it: an OpenAI-compatible API reports usage per call and has
	// no per-tool granularity. It is 0 until the call that follows reports its
	// usage, and stays 0 — meaning "not reported" — when the upstream reports
	// none, so a renderer omits the figure rather than showing a zero.
	Tokens int
	// Result is what the tool returned, as the backend captured it, bounded
	// upstream so that a large output does not ride on every event of a run. It
	// is the ground truth for what an invocation did: a run may describe its own
	// work in prose, and this is the record it can be held to. Empty while the
	// call is pending, and when nothing was returned.
	Result string
}

// Usage is the token usage a run has accumulated.
//
// The totals cover every model call the run made, including each round of a tool
// loop, so prompt tokens are counted once per round: the total is what the run
// consumed, not the size of its final context.
type Usage struct {
	// Calls is the number of model calls the totals cover.
	Calls int
	// PromptTokens is the accumulated prompt token count.
	PromptTokens int
	// CompletionTokens is the accumulated completion token count.
	CompletionTokens int
	// TotalTokens is the accumulated total, prompt plus completion.
	TotalTokens int
	// ReasoningTokens is the accumulated reasoning token count.
	ReasoningTokens int
}

// Interrupt kind values carried in Interrupt.Kind.
const (
	// InterruptKindPermission is a yes/no approval prompt (allow/deny/allow-all).
	InterruptKindPermission = "permission"
	// InterruptKindQuestion is an AskUserQuestion prompt with selectable options.
	InterruptKindQuestion = "question"
)

// InterruptRequest is one call a paused run is waiting on.
type InterruptRequest struct {
	// Address is the interrupt point's fully-qualified ID, generated by the
	// backend for this pause. It is what an answer targets, and it is carried
	// back unread: reconstructing it is not possible from anything this side
	// knows, and a wrong one resumes nothing.
	Address string
	// ToolName is the tool whose execution is pending, for a permission request.
	ToolName string
	// Question is the human-readable prompt shown to the user.
	Question string
	// Kind is InterruptKindPermission or InterruptKindQuestion.
	Kind string
	// Options are the selectable answers for a question request.
	Options []string
	// MultiSelect indicates the user may pick multiple options.
	MultiSelect bool
	// Arguments is the JSON the backend's model asked to run the tool with. It is
	// shown beside the tool name so that approving means approving a specific
	// call, and it is untrusted text: whoever renders it puts it in front of
	// people, so redacting it is the renderer's job.
	Arguments string
}

// Interrupt describes a human-decision pause (for EventInterrupted): the run
// halted until someone answers, and it stays halted until they do.
//
// There is no run id here on purpose. The caller generated the run id it passed
// in AskRequest and is the only party that needs it — it is what the approval
// card carries back and what AnswerPermission addresses — so taking it from the
// event would mean trusting the backend to echo something this side already
// knows, and a mismatch would produce buttons that answer a run nobody is
// watching. The request id is the opposite case and is here for the opposite
// reason: it is generated on the other side, and an answer that does not carry
// it back cannot be tied to the pause it was made against.
type Interrupt struct {
	// RequestID identifies this pause, not the run: a run that pauses twice has
	// two. Answering must name it, so that a card clicked after the run has moved
	// on is refused rather than applied to whatever it waits on now.
	RequestID string
	// Requests are the pending calls, one per interrupt point. A single assistant
	// message can call several tools at once and more than one of them may need a
	// decision, so the group is presented together and answered with one.
	Requests []InterruptRequest
}

// Permissions returns the pending calls that need a yes/no decision.
func (i *Interrupt) Permissions() []InterruptRequest {
	if i == nil {
		return nil
	}
	var out []InterruptRequest
	for _, r := range i.Requests {
		if r.Kind == InterruptKindQuestion {
			continue
		}
		out = append(out, r)
	}
	return out
}

// FirstQuestion returns the first pending call that needs an answer, or a zero
// value when the pause is not asking anything.
//
// One at a time, deliberately: a question is answered with free text that has to
// be attached to one address, and a group of them would need a form rather than
// a card. The rest are not lost — an unanswered question re-interrupts, so the
// next pause asks it.
func (i *Interrupt) FirstQuestion() (InterruptRequest, bool) {
	if i == nil {
		return InterruptRequest{}, false
	}
	for _, r := range i.Requests {
		if r.Kind == InterruptKindQuestion {
			return r, true
		}
	}
	return InterruptRequest{}, false
}

// Event is a single item in a run's event stream. It is backend-neutral so the
// same rendering serves an answer that came from oai-agent-apiserver and one that came
// from a local agent process.
type Event struct {
	Type EventType
	// Delta is the incremental text for EventReasoning and EventAnswer.
	Delta string
	// Steps is the tool log for EventProgress and EventToolStep.
	Steps []ToolStep
	// Final is the complete answer carried by EventDone.
	Final string
	// Err is the cause carried by EventFailed.
	Err error
	// Interrupt describes the pause for EventInterrupted.
	Interrupt *Interrupt
	// Usage is the run's accumulated token usage, carried by EventUsage and by
	// the terminal EventDone, where it is the authoritative total.
	Usage *Usage
	// Images are the generated image references, carried by EventImage.
	Images []media.Ref
}
