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
	"github.com/onexstack/onexai/pkg/media"
)

// consoleSSESink renders the same event stream as the shape the management
// console consumes: `data: {"type":"delta", …}` with a **string** type.
//
// # Why a second sink rather than a flag on the first
//
// The two shapes have different consumers and different contracts. The chat
// service reads `AgentStreamEvent`, whose `type` is the generated enum — a
// number, and the number is the enum's declaration order. A browser that had to
// know "3 means reasoning" would make that order part of its contract: inserting
// a member in the middle of the enum would change what the console renders,
// silently, with nothing failing to compile.
//
// So the console gets its own vocabulary (delta/step/usage/interrupt/suspended/
// final/error), and this file is the only place the two are bridged. The
// frontend's parser (`apps/agent/src/api/stream.ts`) and this file must change
// together; there is no generator for this pair.
type consoleSSESink struct {
	w         io.Writer
	flusher   interface{ Flush() }
	runID     string
	sendChunk func(payload map[string]any) error
}

// NewConsoleSSESink returns a StreamSink that writes the console's string-typed
// events to w.
func NewConsoleSSESink(w io.Writer, flusher interface{ Flush() }, runID string) StreamSink {
	s := &consoleSSESink{w: w, flusher: flusher, runID: runID}
	s.sendChunk = func(payload map[string]any) error {
		// runID on every event: the console needs it to call approve/stop, and a
		// stream is the only place it learns the value.
		payload["runID"] = s.runID
		bytes, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal console stream event: %w", err)
		}
		return writeSSEFrame(s.w, s.flusher, bytes)
	}
	return s
}

func (s *consoleSSESink) Progress(_ context.Context, steps []agent.ToolStep) error {
	out := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		item := map[string]any{
			"name":   step.Name,
			"status": step.Status,
		}
		if step.Duration != 0 {
			item["durationMs"] = step.Duration.Milliseconds()
		}
		if step.Tokens != 0 {
			item["tokens"] = step.Tokens
		}
		if step.ID != "" {
			item["id"] = step.ID
		}
		// Result is forwarded here, and that is a deliberate difference from the
		// chat face: its sink converts the step to the generated proto message,
		// which has no field for the result, so the chat service never sees one.
		// The value itself is populated either way (see agent.ToolStep.Result), so
		// dropping it a second time here would be a choice, not a limitation — and
		// it would cost the console the one piece of evidence that shows what a
		// tool actually did rather than what the model said it did.
		if step.Result != "" {
			item["result"] = step.Result
		}
		out = append(out, item)
	}
	return s.sendChunk(map[string]any{"type": "step", "steps": out})
}

func (s *consoleSSESink) Reasoning(_ context.Context, delta string) error {
	return s.sendChunk(map[string]any{"type": "delta", "text": delta, "reasoning": true})
}

func (s *consoleSSESink) Answer(_ context.Context, delta string) error {
	return s.sendChunk(map[string]any{"type": "delta", "text": delta, "reasoning": false})
}

func (s *consoleSSESink) Interrupted(_ context.Context, it *agent.Interrupt) error {
	if it == nil {
		return nil
	}
	requests := make([]map[string]any, 0, len(it.Requests))
	for _, req := range it.Requests {
		requests = append(requests, map[string]any{
			"address":     req.Address,
			"toolName":    req.ToolName,
			"question":    req.Question,
			"options":     req.Options,
			"multiSelect": req.MultiSelect,
			"kind":        req.Kind,
			"arguments":   req.Arguments,
		})
	}
	return s.sendChunk(map[string]any{
		"type":      "interrupt",
		"interrupt": map[string]any{"requestID": it.RequestID, "requests": requests},
	})
}

// Image tells the console the run produced pictures, as references.
//
// The console does not render them: it is a text pane, and it has no session
// against the media store. The event still goes out, because a console that
// dropped it would leave whoever is watching disagree with the card the same
// run drew for the person in Lark.
func (s *consoleSSESink) Image(_ context.Context, refs []media.Ref) error {
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.String())
	}
	return s.sendChunk(map[string]any{"type": "image", "images": ids})
}

// Sources reports that the run read documents, as references.
//
// The console renders none of them: it is a text pane for watching a run, not a
// surface an answer's provenance is read on. It returns nil rather than an error
// for the reason the interface gives, and — unlike Image, whose event this sink
// does send — it sends nothing: this event has no parser on that surface to
// change alongside it, so a frame here would be one nothing reads.
func (s *consoleSSESink) Sources(_ context.Context, _ []agent.SourceRef) error {
	return nil
}

func (s *consoleSSESink) Usage(_ context.Context, u *agent.Usage) error {
	if u == nil {
		return nil
	}
	return s.sendChunk(map[string]any{
		"type": "usage",
		"usage": map[string]any{
			"promptTokens":     u.PromptTokens,
			"completionTokens": u.CompletionTokens,
			"totalTokens":      u.TotalTokens,
			"reasoningTokens":  u.ReasoningTokens,
			"calls":            u.Calls,
			// The cache split travels on both event shapes: the two sinks differ
			// in how they spell an event, not in which facts a run reports.
			"cacheHitTokens":  u.CacheHitTokens,
			"cacheMissTokens": u.CacheMissTokens,
		},
	})
}

func (s *consoleSSESink) Done(_ context.Context, finalAnswer string) error {
	return s.sendChunk(map[string]any{"type": "final", "text": finalAnswer, "stopped": false})
}

func (s *consoleSSESink) Failed(_ context.Context, err error) error {
	message := "运行失败，上游未给出原因"
	if err != nil {
		message = err.Error()
	}
	return s.sendChunk(map[string]any{"type": "error", "error": message})
}

// Stopped sends an empty text on purpose: the partial answer already reached the
// console as deltas, and replacing it with "" would erase what the model had
// managed to say. The console treats a final event with empty text as "nothing
// more to append".
func (s *consoleSSESink) Stopped(_ context.Context) error {
	return s.sendChunk(map[string]any{"type": "final", "text": "", "stopped": true})
}

// Suspended is its own type rather than a final with a flag, because the run is
// not over: a human still has a pause to answer, and a console that painted
// "finished" here would hide the only action left.
func (s *consoleSSESink) Suspended(_ context.Context) error {
	return s.sendChunk(map[string]any{"type": "suspended"})
}
