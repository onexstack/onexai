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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

// runOpenAISink feeds events through Pump and Close, returning the rendered
// stream.
func runOpenAISink(t *testing.T, events []agent.Event) string {
	t.Helper()
	var buf bytes.Buffer
	sink := NewOpenAIChunkSink(&buf, nil, ChunkMeta{
		ID:        "chatcmpl-test",
		Model:     "test-model",
		CreatedAt: 1,
		SessionID: "session-test",
	}, "该请求需要人工确认")
	ch := make(chan agent.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	if err := Pump(context.Background(), ch, sink); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.String()
}

// parseChunks splits a rendered stream into chunks and reports whether the
// [DONE] marker was present.
func parseChunks(t *testing.T, out string) ([]*v1.ChatCompletionStreamResponse, bool) {
	t.Helper()
	var chunks []*v1.ChatCompletionStreamResponse
	sawDone := false
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			t.Fatalf("unexpected SSE line %q", line)
		}
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		var chunk v1.ChatCompletionStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("decode chunk %q: %v", data, err)
		}
		chunks = append(chunks, &chunk)
	}
	return chunks, sawDone
}

func TestOpenAIChunkSinkRendersAFinishedRun(t *testing.T) {
	out := runOpenAISink(t, []agent.Event{
		{Type: agent.EventReasoning, Delta: "thinking"},
		{Type: agent.EventAnswer, Delta: "he"},
		{Type: agent.EventImage, Images: []media.Ref{{AssetID: "img_1"}}},
		{Type: agent.EventUsage, Usage: &agent.Usage{PromptTokens: 3}},
		{Type: agent.EventDone, Final: "he"},
	})
	chunks, done := parseChunks(t, out)
	if !done {
		t.Fatal("a finished run must end with [DONE]")
	}
	if len(chunks) != 4 {
		t.Fatalf("chunks = %d, want 4 (role, answer, image, finish): %s", len(chunks), out)
	}
	if got := chunks[0].Choices[0].Delta.Role; got != "assistant" {
		t.Errorf("first chunk role = %q, want assistant", got)
	}
	if got := chunks[1].Choices[0].Delta.Content; got != "he" {
		t.Errorf("answer chunk = %q, want he", got)
	}
	if got := len(chunks[2].Choices[0].Delta.ImageRefs); got != 1 {
		t.Errorf("image chunk has %d refs, want 1", got)
	}
	if got := chunks[3].Choices[0].FinishReason; got != "stop" {
		t.Errorf("finish reason = %q, want stop", got)
	}
}

// An engine that emits no deltas still has an answer, and the compatible
// surface must not answer with an empty stream. This is the case the old
// hand-written loop dropped on the floor.
func TestOpenAIChunkSinkFallsBackToTheFinalAnswer(t *testing.T) {
	out := runOpenAISink(t, []agent.Event{{Type: agent.EventDone, Final: "hello"}})
	chunks, done := parseChunks(t, out)
	if !done {
		t.Fatal("missing [DONE]")
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3 (role, final, finish): %s", len(chunks), out)
	}
	if got := chunks[1].Choices[0].Delta.Content; got != "hello" {
		t.Errorf("final answer chunk = %q, want hello", got)
	}
}

func TestOpenAIChunkSinkRendersAPausedRun(t *testing.T) {
	out := runOpenAISink(t, []agent.Event{
		{Type: agent.EventInterrupted},
		{Type: agent.EventSuspended},
	})
	chunks, done := parseChunks(t, out)
	if done {
		t.Error("a paused run must not end with [DONE]: it did not finish")
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3 (role, paused text, finish): %s", len(chunks), out)
	}
	if got := chunks[1].Choices[0].Delta.Content; got != "该请求需要人工确认" {
		t.Errorf("paused chunk = %q", got)
	}
	if got := chunks[2].Choices[0].FinishReason; got != "error" {
		t.Errorf("finish reason = %q, want error", got)
	}
}

// A stream that closes after the interrupt but before the suspended terminal
// still has to tell the caller there is no answer coming.
func TestOpenAIChunkSinkCloseAnswersAnInterruptedStream(t *testing.T) {
	out := runOpenAISink(t, []agent.Event{{Type: agent.EventInterrupted}})
	chunks, done := parseChunks(t, out)
	if done {
		t.Error("an interrupted run must not end with [DONE]")
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3 (role, paused text, finish): %s", len(chunks), out)
	}
	if got := chunks[2].Choices[0].FinishReason; got != "error" {
		t.Errorf("finish reason = %q, want error", got)
	}
}

func TestOpenAIChunkSinkRendersAFailedRun(t *testing.T) {
	out := runOpenAISink(t, []agent.Event{{Type: agent.EventFailed, Err: errors.New("boom")}})
	chunks, done := parseChunks(t, out)
	if done {
		t.Error("a failed run must not end with [DONE]")
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2 (role, finish): %s", len(chunks), out)
	}
	if got := chunks[1].Choices[0].FinishReason; got != "error" {
		t.Errorf("finish reason = %q, want error", got)
	}
}

// A stopped run delivered its partial text but did not finish. The protocol has
// no "cancelled", so it closes the way a failed run does — error, no [DONE] —
// rather than presenting a truncated answer as a complete one.
func TestOpenAIChunkSinkRendersAStoppedRun(t *testing.T) {
	out := runOpenAISink(t, []agent.Event{
		{Type: agent.EventAnswer, Delta: "partial"},
		{Type: agent.EventStopped},
	})
	chunks, done := parseChunks(t, out)
	if done {
		t.Error("a stopped run must not end with [DONE]: it did not finish")
	}
	if got := chunks[1].Choices[0].Delta.Content; got != "partial" {
		t.Errorf("partial answer = %q, want it delivered before the terminal frame", got)
	}
	if got := chunks[len(chunks)-1].Choices[0].FinishReason; got != "error" {
		t.Errorf("finish reason = %q, want error", got)
	}
}

// An empty stream still closes: otherwise a caller waits for an ending that
// already happened.
func TestOpenAIChunkSinkClosesAnEmptyStream(t *testing.T) {
	out := runOpenAISink(t, nil)
	chunks, done := parseChunks(t, out)
	if !done {
		t.Error("an empty stream must still end with [DONE]")
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2 (role, finish): %s", len(chunks), out)
	}
}

// errWriter fails every write, standing in for a connection the client closed.
type errWriter struct{ calls int }

func (w *errWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, errors.New("broken pipe")
}

// A broken connection is one event: later events must not keep writing into it,
// and Close must report the same sticky error rather than inventing a new one.
func TestOpenAIChunkSinkStopsWritingAfterAFailure(t *testing.T) {
	w := &errWriter{}
	sink := NewOpenAIChunkSink(w, nil, ChunkMeta{}, "")
	if w.calls != 1 {
		t.Fatalf("constructor wrote %d times, want 1 (the role frame)", w.calls)
	}
	if err := sink.Answer(context.Background(), "x"); err == nil {
		t.Fatal("Answer after a failed role frame: want the sticky error")
	}
	before := w.calls
	if err := sink.Answer(context.Background(), "y"); err == nil {
		t.Fatal("second Answer: want the sticky error")
	}
	if err := sink.Close(); err == nil {
		t.Fatal("Close: want the sticky error")
	}
	if w.calls != before {
		t.Errorf("kept writing after a failure: %d -> %d writes", before, w.calls)
	}
}

// A stopped run has no final answer, but the deltas it produced are still
// available to a single-body caller — under Streamed, never as Answer.
func TestCollectKeepsWhatAStoppedRunStreamed(t *testing.T) {
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Type: agent.EventAnswer, Delta: "he"}
	ch <- agent.Event{Type: agent.EventStopped}
	close(ch)

	got := Collect(context.Background(), ch)
	if got.Answer != "" {
		t.Errorf("Answer = %q, want empty: the run produced no final answer", got.Answer)
	}
	if got.Streamed != "he" {
		t.Errorf("Streamed = %q, want he", got.Streamed)
	}
	if !got.Stopped {
		t.Error("Stopped = false, want true")
	}
}

// TestCollectTellsTheThreeEndingsApart pins the distinction the
// OpenAI-compatible endpoint's single-body response is built on: a paused run
// is still parked, and its partial text must not be reported as an answer.
func TestCollectTellsTheThreeEndingsApart(t *testing.T) {
	runErr := errors.New("the model backend is unreachable")

	tests := []struct {
		name        string
		events      []agent.Event
		wantAnswer  string
		wantImages  int
		wantErr     error
		wantPaused  bool
		wantStopped bool
	}{
		{
			name:       "a finished run reports its answer",
			events:     []agent.Event{{Type: agent.EventAnswer, Delta: "he"}, {Type: agent.EventDone, Final: "hello"}},
			wantAnswer: "hello",
		},
		{
			name: "a drawn image is collected",
			events: []agent.Event{
				{Type: agent.EventAnswer, Delta: "画好了"},
				{Type: agent.EventImage, Images: []media.Ref{{AssetID: "img_1"}, {AssetID: "img_2"}}},
				{Type: agent.EventDone, Final: "画好了"},
			},
			wantAnswer: "画好了",
			wantImages: 2,
		},
		{
			name:    "a failed run reports the failure",
			events:  []agent.Event{{Type: agent.EventAnswer, Delta: "he"}, {Type: agent.EventFailed, Err: runErr}},
			wantErr: runErr,
		},
		{
			name:       "a suspended run reads as paused",
			events:     []agent.Event{{Type: agent.EventInterrupted}, {Type: agent.EventSuspended}},
			wantPaused: true,
		},
		{
			name:       "a paused run does not report the text it had so far",
			events:     []agent.Event{{Type: agent.EventAnswer, Delta: "he"}, {Type: agent.EventSuspended}},
			wantAnswer: "",
			wantPaused: true,
		},
		{
			name:        "a stopped run is distinguished from an empty one",
			events:      []agent.Event{{Type: agent.EventStopped}},
			wantStopped: true,
		},
		{
			name: "an empty stream is none of the three",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan agent.Event, len(tt.events))
			for _, ev := range tt.events {
				ch <- ev
			}
			close(ch)

			got := Collect(context.Background(), ch)
			if got.Answer != tt.wantAnswer {
				t.Errorf("answer = %q, want %q", got.Answer, tt.wantAnswer)
			}
			if len(got.Images) != tt.wantImages {
				t.Errorf("images = %d, want %d", len(got.Images), tt.wantImages)
			}
			if !errors.Is(got.Err, tt.wantErr) {
				t.Errorf("runErr = %v, want %v", got.Err, tt.wantErr)
			}
			if got.Paused != tt.wantPaused {
				t.Errorf("paused = %v, want %v", got.Paused, tt.wantPaused)
			}
			if got.Stopped != tt.wantStopped {
				t.Errorf("stopped = %v, want %v", got.Stopped, tt.wantStopped)
			}
		})
	}
}
