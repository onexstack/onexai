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
)

// TestGenerateParsesReasoningContent pins the non-streamed half of the
// transport: an upstream that answers with reasoning_content must hand it on in
// the field eino models it with, alongside the content and the finish reason.
func TestGenerateParsesReasoningContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Usage is present because ResponseMeta is only built alongside it — the
		// finish reason rides in the same block.
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"9.9 大",` +
			`"reasoning_content":"先看十分位"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`))
	}))
	defer srv.Close()

	m := NewOpenAIChatModel(srv.URL, "k", "test-model")
	message, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("9.11 和 9.9 哪个大")})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if got := message.ReasoningContent; got != "先看十分位" {
		t.Fatalf("ReasoningContent = %q, want 先看十分位", got)
	}
	if got := message.Content; got != "9.9 大" {
		t.Fatalf("Content = %q, want 9.9 大", got)
	}
	if message.ResponseMeta == nil || message.ResponseMeta.FinishReason != "stop" {
		t.Fatalf("FinishReason lost: %+v", message.ResponseMeta)
	}
}

// TestStreamCarriesReasoningContent pins the streamed shape: one upstream chunk
// becomes at most one message, content and reasoning travel together, and a
// reasoning-only chunk is a message whose Content is empty.
//
// The role-only chunk is the one that must produce nothing — providers open a
// stream with it, and turning it into a message would hand eino an empty chunk
// per call.
func TestStreamCarriesReasoningContent(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
		``,
		`data: {"choices":[{"delta":{"reasoning_content":"先"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"答","reasoning_content":"想"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"案"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	m := NewOpenAIChatModel(srv.URL, "k", "test-model")
	sr, err := m.Stream(context.Background(), []*schema.Message{schema.UserMessage("9.11 和 9.9 哪个大")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	type pair struct{ content, reasoning string }
	var got []pair
	for {
		message, err := sr.Recv()
		if err != nil {
			if err != io.EOF {
				t.Fatalf("Recv: %v", err)
			}
			break
		}
		got = append(got, pair{message.Content, message.ReasoningContent})
	}

	want := []pair{
		{"", "先"},
		{"答", "想"},
		{"案", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("streamed %d messages (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestStreamReasoningDoesNotReachToolCallMessage is the no-duplication pin.
//
// Reasoning and content deltas are streamed as their own messages, so the
// trailing tool-call message must carry neither: eino concatenates the whole
// stream back into one assistant message, and a copy here would double the
// answer and the thinking in the session that follows.
func TestStreamReasoningDoesNotReachToolCallMessage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"先想想"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"查一下"}}]}`,
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

	m := NewOpenAIChatModel(srv.URL, "k", "test-model")
	sr, err := m.Stream(context.Background(), []*schema.Message{schema.UserMessage("what time is it")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	var reasoning, content strings.Builder
	sawToolCalls := false
	for {
		message, err := sr.Recv()
		if err != nil {
			if err != io.EOF {
				t.Fatalf("Recv: %v", err)
			}
			break
		}
		if len(message.ToolCalls) == 0 {
			reasoning.WriteString(message.ReasoningContent)
			content.WriteString(message.Content)
			continue
		}
		sawToolCalls = true
		if message.Content != "" || message.ReasoningContent != "" {
			t.Fatalf("the tool-call message repeated already-streamed text: "+
				"content=%q reasoning=%q", message.Content, message.ReasoningContent)
		}
	}

	if !sawToolCalls {
		t.Fatal("the streamed tool call was dropped")
	}
	if got := reasoning.String(); got != "先想想" {
		t.Fatalf("streamed reasoning = %q, want 先想想", got)
	}
	if got := content.String(); got != "查一下" {
		t.Fatalf("streamed content = %q, want 查一下", got)
	}
}
