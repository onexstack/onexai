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

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
)

// TestResponderWritesIntoTheTreeItResolves is the guard for the one thing that
// makes this bridge work at all.
//
// cobrax calls the command factory and then redirects the root it got back with
// SetOut, draining that pipe as the tool's result. A responder that captured a
// writer up front, or that resolves a different root, writes to the real stdout
// instead: the tool returns no text and the reply is lost in the server's log.
// The test mirrors cobrax's order — build the responder, then redirect the
// root, then write — because that order is the whole point.
func TestResponderWritesIntoTheTreeItResolves(t *testing.T) {
	var root *cobra.Command
	out := newResponder(func() *cobra.Command { return root })
	root = &cobra.Command{Use: "job"}

	// Redirecting after construction is what cobrax does. Resolving the writer
	// at write time is what makes it land here.
	var captured bytes.Buffer
	root.SetOut(&captured)

	if _, err := out.Write([]byte("job-123 listed")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if got := captured.String(); got != "job-123 listed" {
		t.Errorf("captured %q, want %q", got, "job-123 listed")
	}
}

// TestResponderResolvesLateWithoutARoot pins the construction-time guard: the
// responder is passed to the tree builder, so it can be written through before
// the root it resolves has been assigned. Discarding is the intended behaviour
// — the alternative is a nil dereference that takes the process down.
func TestResponderResolvesLateWithoutARoot(t *testing.T) {
	out := newResponder(func() *cobra.Command { return nil })

	if _, err := out.Write([]byte("dropped")); err != nil {
		t.Fatalf("Write returned error before the root existed: %v", err)
	}
	if err := out.SendText(context.Background(), "dropped"); err != nil {
		t.Fatalf("SendText returned error before the root existed: %v", err)
	}
}

// TestResponderRedactsEveryPath checks the masking on each way text leaves a
// command. A tool result is read by a model that may not be running on this
// host, so a secret a command echoed has to be masked here rather than merely
// kept off a Lark card.
//
// Only the two shapes larkcard's redactor actually recognises are asserted: a
// credential key with a prefix word ("api_token=", "client_secret=") and an
// auth header. The plainest spellings — "token=", "api_key=", "password=" —
// pass through it unchanged, because the key pattern requires at least one
// character ahead of the keyword. Widening it is a change to a utility shared
// with Lark card rendering and is not made here; see the note in
// larkcard.RedactInlineSecrets's test coverage. This test pins the plumbing —
// that every output path is routed through the redactor at all — not the
// redactor's reach.
func TestResponderRedactsEveryPath(t *testing.T) {
	root := &cobra.Command{Use: "job"}
	var captured bytes.Buffer
	root.SetOut(&captured)
	out := newResponder(func() *cobra.Command { return root })

	for _, tc := range []struct {
		name string
		text string
		emit func(string) error
	}{
		{
			name: "Write",
			text: "api_token=abc123",
			emit: func(s string) error { _, err := out.Write([]byte(s)); return err },
		},
		{
			name: "SendText",
			text: "client_secret=abc123",
			emit: func(s string) error { return out.SendText(context.Background(), s) },
		},
		{
			name: "SendMarkdownCard",
			text: "Authorization: Bearer abc123",
			emit: func(s string) error {
				return out.SendMarkdownCard(context.Background(), "", s, "")
			},
		},
		{
			name: "SendResult",
			text: "access_token=abc123",
			emit: func(s string) error {
				return out.SendResult(context.Background(), &runtime.CommandResult{PlainText: s})
			},
		},
	} {
		captured.Reset()
		if err := tc.emit(tc.text); err != nil {
			t.Fatalf("%s returned error: %v", tc.name, err)
		}
		if strings.Contains(captured.String(), "abc123") {
			t.Errorf("%s leaked the secret: %q", tc.name, captured.String())
		}
		if !strings.Contains(captured.String(), "[redacted]") {
			t.Errorf("%s did not mark the value as redacted: %q", tc.name, captured.String())
		}
	}
}

// TestSendResultPrefersRaw pins the rendering rule. The commands that set Raw
// hold a domain struct there precisely because the card and the one-line
// summary both lose fields, and a model that is going to reason about the
// result — count it, re-query it, act on an id — does better with the JSON.
func TestSendResultPrefersRaw(t *testing.T) {
	root := &cobra.Command{Use: "job"}
	var captured bytes.Buffer
	root.SetOut(&captured)
	out := newResponder(func() *cobra.Command { return root })

	res := &runtime.CommandResult{
		Raw:       map[string]any{"total": 2},
		PlainText: "2 jobs",
		Markdown:  &runtime.MarkdownContent{Title: "Jobs", Content: "two of them"},
	}
	if err := out.SendResult(context.Background(), res); err != nil {
		t.Fatalf("SendResult returned error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(captured.Bytes(), &decoded); err != nil {
		t.Fatalf("SendResult did not emit JSON (%q): %v", captured.String(), err)
	}
	if decoded["total"] != float64(2) {
		t.Errorf("emitted %v, want the Raw value", decoded)
	}
	if strings.Contains(captured.String(), "two of them") {
		t.Errorf("Raw was set but the markdown was emitted instead: %q", captured.String())
	}
}

// TestSendResultFallsBackInOrder covers a command that fills only the human
// fields. Without a fallback its reply would be empty, which reads to a model
// as a tool that did nothing.
func TestSendResultFallsBackInOrder(t *testing.T) {
	root := &cobra.Command{Use: "job"}
	var captured bytes.Buffer
	root.SetOut(&captured)
	out := newResponder(func() *cobra.Command { return root })

	if err := out.SendResult(context.Background(), &runtime.CommandResult{
		Markdown: &runtime.MarkdownContent{Title: "标题", Content: "正文"},
	}); err != nil {
		t.Fatalf("SendResult returned error: %v", err)
	}
	if got := captured.String(); got != "标题\n\n正文" {
		t.Errorf("markdown fell back to %q, want title and body joined", got)
	}

	captured.Reset()
	if err := out.SendResult(context.Background(), &runtime.CommandResult{PlainText: "纯文本"}); err != nil {
		t.Fatalf("SendResult returned error: %v", err)
	}
	if got := captured.String(); got != "纯文本" {
		t.Errorf("plain-text fallback emitted %q", got)
	}

	// A result with nothing set is not an error and writes nothing.
	captured.Reset()
	if err := out.SendResult(context.Background(), &runtime.CommandResult{}); err != nil {
		t.Fatalf("SendResult returned error: %v", err)
	}
	if captured.Len() != 0 {
		t.Errorf("an empty result emitted %q", captured.String())
	}
}
