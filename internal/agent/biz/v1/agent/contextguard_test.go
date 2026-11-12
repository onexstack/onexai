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
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// hugeOutput builds a message body whose head and tail are distinguishable, so
// a test can tell which end of a truncated output it is looking at.
func hugeOutput(head, tail int) string {
	return strings.Repeat("A", head) + strings.Repeat("B", tail)
}

// splitTruncated splits a rewritten tool output into its head, the notice
// spliced into it, and its tail, without assuming the implementation's offsets.
func splitTruncated(t *testing.T, out string) (head, notice, tail string) {
	t.Helper()

	const marker = "\n\n... [OUTPUT TRUNCATED"
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("no truncation notice in %q", out)
	}
	const noticeTail = "] ...\n\n"
	end := strings.LastIndex(out[idx:], noticeTail)
	if end < 0 {
		t.Fatalf("truncation notice is not terminated in %q", out)
	}

	head = out[:idx]
	notice = out[idx : idx+end+len("] ...")]
	tail = out[idx+end+len(noticeTail):]
	return head, notice, tail
}

// runGuard drives one BeforeModelRewriteState pass and returns the messages.
func runGuard(t *testing.T, h *contextGuardHandler, state *adk.ChatModelAgentState) []adk.Message {
	t.Helper()

	_, got, err := h.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState: %v", err)
	}
	return got.Messages
}

func TestContextGuard_TruncatesOversizedToolOutput(t *testing.T) {
	const maxRunes = 1000
	const original = 10000
	h := newContextGuardHandler(maxRunes)
	message := schema.ToolMessage(hugeOutput(original/2, original/2), "call-1", schema.WithToolName("search"))

	got := runGuard(t, h, &adk.ChatModelAgentState{Messages: []adk.Message{message}})
	out := got[0].Content

	// The cap is the contract: the message must not exceed it, notice included.
	if n := len([]rune(out)); n > maxRunes {
		t.Fatalf("rewritten output is %d runes, over the %d cap", n, maxRunes)
	}

	head, notice, tail := splitTruncated(t, out)
	if len(head) == 0 || strings.Trim(head, "A") != "" {
		t.Fatalf("head was not preserved intact: %q", head)
	}
	if len(tail) == 0 || strings.Trim(tail, "B") != "" {
		t.Fatalf("tail was not preserved intact: %q", tail)
	}
	// The tail is favoured because errors and conclusions usually land at the
	// end of a tool's output.
	if len(tail) <= len(head) {
		t.Fatalf("tail (%d runes) is not larger than head (%d runes)", len(tail), len(head))
	}
	headRunes, tailRunes := len([]rune(head)), len([]rune(tail))
	if headRunes*100 < 25*(headRunes+tailRunes) || headRunes*100 > 35*(headRunes+tailRunes) {
		t.Fatalf("head is %d of %d content runes, want roughly %d%%",
			headRunes, headRunes+tailRunes, int(truncatedHeadRatio*100))
	}

	// The reported count must match what actually went missing.
	elided := original - headRunes - tailRunes
	if !strings.Contains(notice, strconv.Itoa(elided)) {
		t.Fatalf("notice does not report the %d elided runes: %q", elided, notice)
	}

	if len(got) != 1 {
		t.Fatalf("truncation changed the message count: %d", len(got))
	}
	kept := got[0]
	if kept.Role != schema.Tool || kept.ToolCallID != "call-1" || kept.ToolName != "search" {
		t.Fatalf("truncation changed the tool message identity: role=%q id=%q name=%q",
			kept.Role, kept.ToolCallID, kept.ToolName)
	}
}

func TestContextGuard_LeavesShortMessagesAlone(t *testing.T) {
	h := newContextGuardHandler(1000)
	// Exactly at the cap is not over it.
	message := schema.ToolMessage(strings.Repeat("A", 1000), "call-1")

	got := runGuard(t, h, &adk.ChatModelAgentState{Messages: []adk.Message{message}})

	if got[0].Content != message.Content {
		t.Fatalf("an in-budget tool output was rewritten: %d runes", len([]rune(got[0].Content)))
	}
}

func TestContextGuard_CountsRunesNotBytes(t *testing.T) {
	// 300 Chinese characters is 900 bytes: a byte-based guard would truncate it
	// against a 400 cap, a rune-based one leaves it alone.
	h := newContextGuardHandler(400)
	message := schema.ToolMessage(strings.Repeat("中", 300), "call-1")

	got := runGuard(t, h, &adk.ChatModelAgentState{Messages: []adk.Message{message}})

	if got[0].Content != message.Content {
		t.Fatalf("a %d-rune (900-byte) tool output was truncated against a 400-rune cap",
			len([]rune(message.Content)))
	}
}

func TestContextGuard_NeverTouchesNonToolMessages(t *testing.T) {
	h := newContextGuardHandler(1000)
	oversized := hugeOutput(5000, 5000)
	state := &adk.ChatModelAgentState{Messages: []adk.Message{
		schema.UserMessage(oversized),
		schema.AssistantMessage(oversized, nil),
		schema.ToolMessage(oversized, "call-1"),
	}}

	got := runGuard(t, h, state)

	// Dropping a message would orphan the tool call it answers, so the guard
	// only ever truncates: the count is invariant.
	if len(got) != 3 {
		t.Fatalf("message count changed from 3 to %d", len(got))
	}
	if got[0].Content != oversized || got[1].Content != oversized {
		t.Fatal("a user or assistant message was rewritten")
	}
	if got[2].Content == oversized {
		t.Fatal("the oversized tool message was left untouched")
	}
}

func TestContextGuard_IsIdempotent(t *testing.T) {
	h := newContextGuardHandler(1000)
	message := schema.ToolMessage(hugeOutput(5000, 5000), "call-1")
	state := &adk.ChatModelAgentState{Messages: []adk.Message{message}}

	// This hook runs before every model call of a run, so a multi-iteration run
	// passes the same message through it repeatedly.
	first := runGuard(t, h, state)[0].Content
	for pass := 2; pass <= 4; pass++ {
		got := runGuard(t, h, &adk.ChatModelAgentState{Messages: []adk.Message{message}})[0].Content
		if got != first {
			t.Fatalf("pass %d rewrote the output again:\nfirst: %q\npass:  %q", pass, first, got)
		}
	}
}

func TestContextGuard_TinyCapStillConverges(t *testing.T) {
	// A cap too small to hold the notice itself must still be enforced, and
	// must not re-truncate its own output into nothing on every pass.
	h := newContextGuardHandler(10)
	message := schema.ToolMessage(hugeOutput(500, 500), "call-1")

	first := runGuard(t, h, &adk.ChatModelAgentState{Messages: []adk.Message{message}})[0].Content
	if n := len([]rune(first)); n > 10 {
		t.Fatalf("rewritten output is %d runes, over the 10 cap", n)
	}
	if first == "" {
		t.Fatal("a tiny cap elided the whole output")
	}
	second := runGuard(t, h, &adk.ChatModelAgentState{Messages: []adk.Message{message}})[0].Content
	if second != first {
		t.Fatalf("a tiny cap did not converge:\nfirst:  %q\nsecond: %q", first, second)
	}
}

func TestContextGuard_DisabledWhenZero(t *testing.T) {
	h := newContextGuardHandler(0)
	message := schema.ToolMessage(hugeOutput(500, 500), "call-1")

	got := runGuard(t, h, &adk.ChatModelAgentState{Messages: []adk.Message{message}})

	if got[0].Content != message.Content {
		t.Fatal("a zero cap still truncated the output")
	}

	// A nil state is tolerated rather than panicking.
	if _, _, err := h.BeforeModelRewriteState(context.Background(), nil, nil); err != nil {
		t.Fatalf("BeforeModelRewriteState(nil): %v", err)
	}
}
