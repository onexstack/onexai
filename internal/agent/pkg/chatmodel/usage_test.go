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

package chatmodel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/onexstack/onexai/internal/agent/pkg/usage"
)

// TestBuildRequestAsksForStreamUsage pins the request field that makes token
// usage possible at all on a streamed call: the OpenAI streaming protocol only
// reports usage when the request opts in.
func TestBuildRequestAsksForStreamUsage(t *testing.T) {
	m := &OpenAIChatModel{}

	streamed, err := m.buildRequest(nil, true, "test-model", nil, 0)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if streamed.StreamOptions == nil || !streamed.StreamOptions.IncludeUsage {
		t.Fatalf("a streamed request must ask for usage, got %+v", streamed.StreamOptions)
	}

	// A non-streamed response reports usage unconditionally, so there is nothing
	// to opt into and the field should not be sent.
	plain, err := m.buildRequest(nil, false, "test-model", nil, 0)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if plain.StreamOptions != nil {
		t.Fatalf("a plain request should not carry stream_options: %+v", plain.StreamOptions)
	}

	// The escape hatch for an upstream that rejects the field outright.
	off, err := m.WithStreamUsage(false).buildRequest(nil, true, "test-model", nil, 0)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if off.StreamOptions != nil {
		t.Fatalf("WithStreamUsage(false) should drop stream_options, got %+v", off.StreamOptions)
	}
}

func TestGenerateParsesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"},` +
			`{"message":{"content":"ignored"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,` +
			`"completion_tokens_details":{"reasoning_tokens":7}}}`))
	}))
	defer srv.Close()

	tracker := usage.New()
	m := NewOpenAIChatModel(srv.URL, "k", "test-model")
	message, err := m.Generate(usage.WithTracker(context.Background(), tracker), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		t.Fatalf("ResponseMeta.Usage is missing: %+v", message.ResponseMeta)
	}
	if got := message.ResponseMeta.Usage.TotalTokens; got != 120 {
		t.Fatalf("TotalTokens = %d, want 120", got)
	}
	if got := message.ResponseMeta.Usage.CompletionTokensDetails.ReasoningTokens; got != 7 {
		t.Fatalf("ReasoningTokens = %d, want 7", got)
	}
	if got := message.ResponseMeta.FinishReason; got != "stop" {
		t.Fatalf("FinishReason = %q, want stop", got)
	}

	got := tracker.Totals()
	if got.Calls != 1 || got.PromptTokens != 100 || got.CompletionTokens != 20 ||
		got.TotalTokens != 120 || got.ReasoningTokens != 7 {
		t.Fatalf("recorded %+v, want one call of 100/20/120/7", got)
	}
}

// TestGenerateRecordsUsageWithoutTracker keeps the adapter usable outside an
// agent run: a context without a tracker must not panic or fail the call.
func TestGenerateRecordsUsageWithoutTracker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer srv.Close()

	m := NewOpenAIChatModel(srv.URL, "k", "test-model")
	if _, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")}); err != nil {
		t.Fatalf("Generate without a tracker: %v", err)
	}
}

// TestStreamRecordsUsageBeforeToolCalls is the ordering test.
//
// The agent starts running the tools the moment it sees the assembled tool-call
// message, and the round that follows is what reveals how much context those
// tools added. If the call's usage were recorded after that message was handed
// on, the attribution would pair the tools with the wrong round — so this pins
// the recorder running first.
func TestStreamRecordsUsageBeforeToolCalls(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hel"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function",` +
			`"function":{"name":"current_time","arguments":"{}"}}]}}]}`,
		``,
		`data: {"choices":[],"usage":{"prompt_tokens":300,"completion_tokens":40,"total_tokens":340}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	tracker := usage.New()
	m := NewOpenAIChatModel(srv.URL, "k", "test-model")
	sr, err := m.Stream(usage.WithTracker(context.Background(), tracker), []*schema.Message{schema.UserMessage("what time is it")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	var content strings.Builder
	sawToolCalls := false
	for {
		message, err := sr.Recv()
		if err != nil {
			if err != io.EOF {
				t.Fatalf("Recv: %v", err)
			}
			break
		}
		content.WriteString(message.Content)
		if len(message.ToolCalls) == 0 {
			continue
		}
		sawToolCalls = true
		if got := tracker.Totals().Calls; got != 1 {
			t.Fatalf("the round's usage must be recorded before its tool calls are handed on: "+
				"tracker holds %d calls", got)
		}
		if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
			t.Fatalf("the tool-call message lost the usage: %+v", message.ResponseMeta)
		}
	}

	if !sawToolCalls {
		t.Fatal("the streamed tool call was dropped")
	}
	if got := content.String(); got != "hello" {
		t.Fatalf("content = %q, want hello", got)
	}
	if got := tracker.Totals(); got.Calls != 1 || got.PromptTokens != 300 || got.TotalTokens != 340 {
		t.Fatalf("recorded %+v, want one call of 300/340", got)
	}
}
