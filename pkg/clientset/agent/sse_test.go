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
	"errors"
	"io"
	"strings"
	"testing"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func TestScanSSELines(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantErr    bool
		wantChunks int
	}{
		{
			name: "happy path ends with DONE",
			body: "data: " + `{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n" +
				"data: " + `{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finishReason":"stop"}]}` + "\n\n" +
				"data: [DONE]\n\n",
			wantChunks: 2,
		},
		{
			name:    "error finish reason",
			body:    "data: " + `{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finishReason":"error"}]}` + "\n\n",
			wantErr: true,
		},
		{
			name:    "premature EOF without DONE",
			body:    "data: " + `{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var chunks int
			err := scanSSELines(strings.NewReader(tt.body), func(*v1.ChatCompletionStreamResponse) error {
				chunks++
				return nil
			})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if chunks != tt.wantChunks {
				t.Fatalf("got %d chunks, want %d", chunks, tt.wantChunks)
			}
		})
	}
}

// errReader fails every read with err, which is how a body that ends without its
// final chunk reads: net/http reports io.ErrUnexpectedEOF when the peer closes a
// chunked response mid-stream.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// The wire form carries the enum as a number, not a name: the sink marshals the
// proto with encoding/json, which has no UnmarshalJSON/MarshalJSON on the enum
// to render or accept the name. A fixture using the name is silently skipped by
// scanAgentSSE's unmarshal guard, which is how this constant was first written.
const agentAnswerEvent = `{"type":4,"delta":"hel"}` + "\n\n"

// TestScanAgentSSETruncated pins the difference between the two ways this stream
// can stop with an error, because they reach the caller as very different
// things: a transport truncation must say so, while a run the backend reported
// as failed must keep its own message.
func TestScanAgentSSETruncated(t *testing.T) {
	t.Run("severed body names the transport", func(t *testing.T) {
		var events int
		err := scanAgentSSE(
			io.MultiReader(strings.NewReader("data: "+agentAnswerEvent), errReader{io.ErrUnexpectedEOF}),
			func(*v1.AgentStreamEvent) error { events++; return nil },
		)
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		// The cause survives, so callers can still tell a reset from a timeout.
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("error does not wrap the read failure: %v", err)
		}
		if !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("error does not identify a truncated stream: %v", err)
		}
		if events != 1 {
			t.Fatalf("got %d events before the truncation, want 1", events)
		}
	})

	t.Run("clean EOF is not an error", func(t *testing.T) {
		err := scanAgentSSE(
			strings.NewReader("data: "+agentAnswerEvent),
			func(*v1.AgentStreamEvent) error { return nil },
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("a failed run keeps its own message", func(t *testing.T) {
		runErr := errors.New("upstream refused the request")
		err := scanAgentSSE(
			strings.NewReader("data: "+agentAnswerEvent),
			func(*v1.AgentStreamEvent) error { return runErr },
		)
		if !errors.Is(err, runErr) {
			t.Fatalf("backend error was not propagated: %v", err)
		}
		if strings.Contains(err.Error(), "truncated") {
			t.Fatalf("a backend failure was reported as a transport truncation: %v", err)
		}
	})
}
