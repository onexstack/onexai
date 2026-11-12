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

// Package agentstream renders a backend-neutral agent event stream to a
// destination via the StreamSink interface. Pump drives the events into a sink,
// applying throttling/batching so implementations only handle "real sends".
// Sinks exist for a plain io.Writer (REST/TUI text) and, later, Lark streaming
// cards.
package agentstream

import (
	"context"
	"fmt"
	"io"
	"strings"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

// StreamSink renders agent events to a destination. Answer/Reasoning receive
// incremental deltas; the terminal call is exactly one of Done/Failed/Stopped/
// Suspended.
type StreamSink interface {
	Progress(ctx context.Context, steps []agent.ToolStep) error
	Reasoning(ctx context.Context, delta string) error
	Answer(ctx context.Context, delta string) error
	Interrupted(ctx context.Context, it *agent.Interrupt) error
	// Image reports generated images. A sink with nowhere to render them
	// returns nil rather than being optional: the interface is what makes every
	// destination decide, and a sink that forgot would be a run whose pictures
	// silently vanished.
	Image(ctx context.Context, refs []media.Ref) error
	// Sources reports the documents a retrieval returned. Delivered more than
	// once per run, like Image, and for the same reason: a run may retrieve
	// again after reading what it got.
	//
	// Same rule as Image for a destination with no place to put them: it returns
	// nil, and the interface is what makes that a decision rather than an
	// omission.
	Sources(ctx context.Context, refs []agent.SourceRef) error
	// Usage reports the run's accumulated token usage. It is delivered before
	// Done, whose event carries the same authoritative total.
	Usage(ctx context.Context, u *agent.Usage) error
	Done(ctx context.Context, finalAnswer string) error
	Failed(ctx context.Context, err error) error
	Stopped(ctx context.Context) error
	// Suspended reports that the stream ended because the run paused for a
	// decision. It is terminal for the stream but not for the run: the same run
	// continues when the pause is answered, and the events after that are
	// delivered to whatever sink the continuation is given.
	//
	// A sink that omits it would leave a paused run looking finished, so it is
	// required rather than optional even where there is nothing to render.
	Suspended(ctx context.Context) error
}

// writeSSEFrame writes one `data: ...` frame and flushes it.
//
// Every sink in this package speaks the same framing, and the framing is a
// property of the transport rather than of a surface: a change to it — an
// `event:` line, a keep-alive comment — must reach all of them at once. Keeping
// the write in one helper is what makes that one edit instead of three.
func writeSSEFrame(w io.Writer, flusher interface{ Flush() }, payload []byte) error {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// Pump drives events from the channel into the sink until the channel closes.
// It returns the first sink error encountered.
func Pump(ctx context.Context, events <-chan agent.Event, sink StreamSink) error {
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// The totals most recently delivered, so Done's copy is only sent when it
	// adds information. Done carrying the same total the engine already
	// reported is the ordinary shape, and forwarding both makes every surface
	// render the same numbers twice.
	var lastUsage *agent.Usage
	for ev := range events {
		switch ev.Type {
		case agent.EventProgress, agent.EventToolStep:
			record(sink.Progress(ctx, ev.Steps))
		case agent.EventReasoning:
			record(sink.Reasoning(ctx, ev.Delta))
		case agent.EventAnswer:
			record(sink.Answer(ctx, ev.Delta))
		case agent.EventInterrupted:
			record(sink.Interrupted(ctx, ev.Interrupt))
		case agent.EventImage:
			record(sink.Image(ctx, ev.Images))
		case agent.EventSources:
			record(sink.Sources(ctx, ev.Sources))
		case agent.EventUsage:
			lastUsage = ev.Usage
			record(sink.Usage(ctx, ev.Usage))
		case agent.EventDone:
			// The final total goes out before the terminal event, so a sink that
			// only renders on Done still sees it — but only when the engine did
			// not already report the same total on its own event.
			if !sameUsage(lastUsage, ev.Usage) {
				lastUsage = ev.Usage
				record(sink.Usage(ctx, ev.Usage))
			}
			record(sink.Done(ctx, ev.Final))
		case agent.EventFailed:
			record(sink.Failed(ctx, ev.Err))
		case agent.EventStopped:
			record(sink.Stopped(ctx))
		case agent.EventSuspended:
			record(sink.Suspended(ctx))
		}
	}
	return firstErr
}

// sameUsage reports whether two usage reports carry the same totals. Nil is
// equal only to nil: an absent total is not a zero total.
func sameUsage(a, b *agent.Usage) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// writerSink renders the answer stream to an io.Writer (REST/TUI text). It
// ignores reasoning/progress by default so plain callers see just the answer.
type writerSink struct {
	w             io.Writer
	wroteAnything bool
}

// NewWriterSink returns a StreamSink that streams answer text to w.
func NewWriterSink(w io.Writer) StreamSink { return &writerSink{w: w} }

func (s *writerSink) Progress(context.Context, []agent.ToolStep) error { return nil }
func (s *writerSink) Reasoning(context.Context, string) error          { return nil }

// A plain text writer cannot show a picture. It returns nil rather than an
// error so a run that draws one still delivers its answer.
func (s *writerSink) Image(context.Context, []media.Ref) error { return nil }

// A text stream could print the documents a retrieval read, and none of its
// callers has asked for them: whoever reads this stream is reading an answer,
// and the caller that renders provenance is not reading text. It returns nil
// rather than an error for the same reason Image does — a run that retrieved
// still delivers its answer.
func (s *writerSink) Sources(context.Context, []agent.SourceRef) error { return nil }

func (s *writerSink) Usage(context.Context, *agent.Usage) error { return nil }

// Interrupted renders the pause rather than swallowing it. A caller reading
// plain text has no other way to learn that a tool is waiting on a decision, and
// the alternative — a stream that simply stops — reads as a truncated answer.
func (s *writerSink) Interrupted(_ context.Context, it *agent.Interrupt) error {
	if it == nil || len(it.Requests) == 0 {
		return nil
	}
	_, err := io.WriteString(s.w, interruptText(it))
	return err
}

// interruptText renders a pause as plain text for callers with no card to put it
// on. It names the request so that an HTTP caller can answer the pause it is
// looking at rather than whichever pause the run reached by then.
func interruptText(it *agent.Interrupt) string {
	var b strings.Builder
	b.WriteString("\n[需要人工确认] 请求编号: ")
	b.WriteString(it.RequestID)
	b.WriteString("\n")
	for _, r := range it.Requests {
		if r.Question != "" {
			b.WriteString("- ")
			b.WriteString(r.Question)
			b.WriteString("\n")
			continue
		}
		b.WriteString("- 工具 ")
		b.WriteString(r.ToolName)
		b.WriteString(" 待批准\n")
	}
	return b.String()
}

func (s *writerSink) Answer(_ context.Context, delta string) error {
	if delta == "" {
		return nil
	}
	s.wroteAnything = true
	_, err := io.WriteString(s.w, delta)
	return err
}

func (s *writerSink) Done(_ context.Context, finalAnswer string) error {
	// If nothing streamed incrementally (non-streaming engine), emit the final.
	if !s.wroteAnything && finalAnswer != "" {
		if _, err := io.WriteString(s.w, finalAnswer); err != nil {
			return err
		}
	}
	_, err := io.WriteString(s.w, "\n")
	return err
}

func (s *writerSink) Failed(_ context.Context, err error) error {
	if err == nil {
		return nil
	}
	_, werr := io.WriteString(s.w, "\nAI error: "+err.Error()+"\n")
	return werr
}

func (s *writerSink) Stopped(_ context.Context) error {
	_, err := io.WriteString(s.w, "\n(stopped)\n")
	return err
}

// Suspended closes a pause the way the other terminals close a stream, so that a
// caller reading the text can tell "paused, answer to continue" from "finished".
func (s *writerSink) Suspended(_ context.Context) error {
	_, err := io.WriteString(s.w, "\n(已暂停，等待确认；答复后继续)\n")
	return err
}
