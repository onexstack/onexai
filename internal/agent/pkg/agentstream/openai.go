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
	"log/slog"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// ChunkMeta is the identity every chunk of one completion carries. The values
// are fixed for the whole stream: a client joins chunks by id, and a chunk that
// renamed its model or session would look like a different completion.
type ChunkMeta struct {
	ID        string
	Model     string
	CreatedAt int64
	SessionID string
}

// OpenAIChunkSink renders an agent event stream as OpenAI-compatible
// /chat/completions chunks, ending with the [DONE] marker.
//
// It exists so the compatible surface consumes events the same way every other
// surface does — through Pump and this package's sink contract — instead of
// carrying its own copy of the dispatch switch. That copy is what let this
// endpoint drift: an event it did not name was silently dropped, and a new
// event type had to be remembered here as well as in Pump.
type OpenAIChunkSink struct {
	w          io.Writer
	flusher    interface{ Flush() }
	meta       ChunkMeta
	pausedText string
	// paused records that the run asked for a human decision. The question
	// itself has no place in this protocol; Close answers it with pausedText.
	paused bool
	// wroteAnswer reports whether any answer text was streamed, so Done can
	// fall back to its final value for an engine that only emits the whole
	// message at the end.
	wroteAnswer bool
	// terminal records that a Done/Failed/Stopped/Suspended already closed the
	// stream, so Close does not close it twice.
	terminal bool
	err      error
}

// NewOpenAIChunkSink returns a sink that writes chat.completion.chunk frames to
// w. pausedText is the sentence this surface shows for a run parked on a human
// decision; empty leaves the paused stream closing with an error frame only.
//
// The role frame is written here, once, so the first content frame never has to
// carry it: a caller that joins chunks as they arrive would otherwise see no
// assistant message until the model produced text.
func NewOpenAIChunkSink(w io.Writer, flusher interface{ Flush() }, meta ChunkMeta, pausedText string) *OpenAIChunkSink {
	s := &OpenAIChunkSink{w: w, flusher: flusher, meta: meta, pausedText: pausedText}
	s.emit(&v1.ChatCompletionMessage{Role: "assistant"}, "")
	return s
}

// emit writes one chunk. The first failure is remembered and returned by every
// later call, because a broken connection cannot be repaired by writing more:
// reporting the same error once per event would only fill the log.
func (s *OpenAIChunkSink) emit(delta *v1.ChatCompletionMessage, finishReason string) error {
	if s.err != nil {
		return s.err
	}
	chunk := &v1.ChatCompletionStreamResponse{
		Id:      s.meta.ID,
		Object:  "chat.completion.chunk",
		Created: s.meta.CreatedAt,
		Model:   s.meta.Model,
		Choices: []*v1.ChatCompletionStreamChoice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
		SessionID: s.meta.SessionID,
	}
	body, err := json.Marshal(chunk)
	if err != nil {
		return s.fail(fmt.Errorf("marshal chat completion chunk: %w", err))
	}
	return s.fail(writeSSEFrame(s.w, s.flusher, body))
}

// doneMarker writes the protocol's end-of-stream marker.
func (s *OpenAIChunkSink) doneMarker() error {
	if s.err != nil {
		return s.err
	}
	return s.fail(writeSSEFrame(s.w, s.flusher, []byte("[DONE]")))
}

func (s *OpenAIChunkSink) fail(err error) error {
	if err != nil && s.err == nil {
		s.err = err
	}
	return s.err
}

// Progress and Reasoning have no representation on this surface: the compatible
// protocol carries the answer, and a caller that asked for it is not rendering
// tool steps or the model's thinking.
func (s *OpenAIChunkSink) Progress(context.Context, []agent.ToolStep) error { return nil }
func (s *OpenAIChunkSink) Reasoning(context.Context, string) error          { return nil }

func (s *OpenAIChunkSink) Answer(_ context.Context, delta string) error {
	if delta == "" {
		return nil
	}
	s.wroteAnswer = true
	return s.emit(&v1.ChatCompletionMessage{Content: delta}, "")
}

// Interrupted only records the pause. The question is not part of this
// protocol; Suspended, or Close for a stream that ended without it, is where the
// caller is told there is no answer coming.
func (s *OpenAIChunkSink) Interrupted(context.Context, *agent.Interrupt) error {
	s.paused = true
	return nil
}

// Image rides its own chunk, in the same field a request uses to attach one.
// Without it an OpenAI-compatible caller that asked for an image got only the
// model's words that it had drawn one.
func (s *OpenAIChunkSink) Image(_ context.Context, refs []media.Ref) error {
	return s.emit(&v1.ChatCompletionMessage{ImageRefs: agent.RefsToProto(refs)}, "")
}

// Sources and Usage are not part of the compatible protocol. Usage in
// particular belongs to the caller's own accounting, not to a delta: attaching
// it to a content chunk would make the totals arrive once per chunk.
func (s *OpenAIChunkSink) Sources(context.Context, []agent.SourceRef) error { return nil }
func (s *OpenAIChunkSink) Usage(context.Context, *agent.Usage) error        { return nil }

func (s *OpenAIChunkSink) Done(_ context.Context, finalAnswer string) error {
	s.terminal = true
	// An engine that streams no deltas still has an answer to deliver. This is
	// the one place the compatible surface can recover it without asking the
	// caller to read the non-streaming response instead.
	if !s.wroteAnswer && finalAnswer != "" {
		if err := s.emit(&v1.ChatCompletionMessage{Content: finalAnswer}, ""); err != nil {
			return err
		}
	}
	if err := s.emit(&v1.ChatCompletionMessage{}, "stop"); err != nil {
		return err
	}
	return s.doneMarker()
}

func (s *OpenAIChunkSink) Failed(ctx context.Context, err error) error {
	s.terminal = true
	if err != nil {
		slog.WarnContext(ctx, "chat completion stream error", "error", err)
	}
	// No [DONE]: the stream failed, and the marker is what a client reads as a
	// complete response.
	return s.emit(&v1.ChatCompletionMessage{}, "error")
}

// Stopped closes a run the user stopped.
//
// It is reported like a failed run — finishReason "error", no [DONE] — because
// the answer is incomplete and this protocol has no "cancelled". Calling it
// "stop" made a truncated answer read as a finished one; the partial text is
// still delivered, so a client that renders it keeps what the model managed to
// say while learning that it did not finish.
func (s *OpenAIChunkSink) Stopped(context.Context) error {
	s.terminal = true
	return s.emit(&v1.ChatCompletionMessage{}, "error")
}

// Suspended closes a paused run: this endpoint cannot put the question to
// anyone, so it says so in the only field the protocol has — content — and
// marks the stream as not successful.
func (s *OpenAIChunkSink) Suspended(context.Context) error {
	s.terminal = true
	if s.pausedText != "" {
		if err := s.emit(&v1.ChatCompletionMessage{Content: s.pausedText}, ""); err != nil {
			return err
		}
	}
	return s.emit(&v1.ChatCompletionMessage{}, "error")
}

// Close closes a stream that ended without a terminal event, and otherwise does
// nothing. The caller invokes it after Pump returns.
//
// A stream that just stops is indistinguishable from a truncated response, and
// this surface always closed with a finish frame: a paused run gets its
// pausedText (the interrupt had no protocol representation), and any other
// unfinished stream reads as complete rather than hanging. Without this, an
// event the sink never received would leave the caller waiting for an ending
// that already happened.
func (s *OpenAIChunkSink) Close() error {
	if s.terminal {
		return s.err
	}
	if s.paused {
		return s.Suspended(context.Background())
	}
	s.terminal = true
	if err := s.emit(&v1.ChatCompletionMessage{}, "stop"); err != nil {
		return err
	}
	return s.doneMarker()
}

// Collected is what a non-streaming caller needs from one run.
type Collected struct {
	// Answer is the run's final answer, as reported by its Done event. It is
	// deliberately not the concatenation of the streamed deltas: a run that
	// paused mid-answer never produced a final answer, and its partial text must
	// not be presented as one.
	Answer string
	// Streamed is the text the model produced, in order, whether or not the run
	// finished. It is only used for a run the caller stopped: there is no final
	// answer to report, but the partial one is what the caller asked to stop and
	// is still theirs. A paused run's Streamed must not be presented as an
	// answer — the run is parked, not truncated.
	Streamed string
	Images   []media.Ref
	Usage    *agent.Usage
	// Err is the failure that ended the run, if any.
	Err error
	// Paused reports that the run parked on a human decision, which this caller
	// cannot give.
	Paused bool
	// Stopped reports that the run was cancelled.
	Stopped bool
}

// Collect drains an event stream into the values a single-body response needs.
//
// It is Pump with a sink that keeps rather than renders, so a non-streaming
// surface does not grow its own dispatch switch: every event type is handled in
// exactly one place, and a new one reaches this caller through the sink
// contract instead of being silently dropped.
func Collect(ctx context.Context, events <-chan agent.Event) Collected {
	var out Collected
	_ = Pump(ctx, events, &collectSink{out: &out})
	return out
}

// collectSink keeps the values a single-body response needs instead of
// rendering them.
type collectSink struct {
	out *Collected
}

func (c *collectSink) Progress(context.Context, []agent.ToolStep) error { return nil }
func (c *collectSink) Reasoning(context.Context, string) error          { return nil }

func (c *collectSink) Answer(_ context.Context, delta string) error {
	c.out.Streamed += delta
	return nil
}

func (c *collectSink) Interrupted(context.Context, *agent.Interrupt) error {
	c.out.Paused = true
	return nil
}

func (c *collectSink) Image(_ context.Context, refs []media.Ref) error {
	c.out.Images = append(c.out.Images, refs...)
	return nil
}

func (c *collectSink) Sources(context.Context, []agent.SourceRef) error { return nil }

func (c *collectSink) Usage(_ context.Context, u *agent.Usage) error {
	if u != nil {
		c.out.Usage = u
	}
	return nil
}

func (c *collectSink) Done(_ context.Context, finalAnswer string) error {
	c.out.Answer = finalAnswer
	return nil
}

func (c *collectSink) Failed(_ context.Context, err error) error {
	c.out.Err = err
	return nil
}

func (c *collectSink) Stopped(context.Context) error {
	c.out.Stopped = true
	return nil
}

func (c *collectSink) Suspended(context.Context) error {
	c.out.Paused = true
	return nil
}
