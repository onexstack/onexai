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

package agentcli

import (
	"bufio"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeSessionID(t *testing.T) {
	ev, ok := Decode(`{"type":"system","subtype":"init","session_id":"sess-abc","tools":["Bash"]}`)
	if !ok {
		t.Fatal("an init line must decode")
	}
	if ev.SessionID != "sess-abc" {
		t.Errorf("SessionID = %q, want %q", ev.SessionID, "sess-abc")
	}
	// The session is the init event's to report. Accepting a session_id from any
	// line that happens to carry one would accept a session a later event has
	// already moved on from.
	ev, ok = Decode(`{"type":"assistant","session_id":"sess-other","message":{"content":[]}}`)
	if !ok {
		t.Fatal("an assistant line must decode")
	}
	if ev.SessionID != "sess-other" {
		t.Fatalf("the field decodes, but SessionID(line) must not report it")
	}
	if got := SessionID(`{"type":"assistant","session_id":"sess-other","message":{"content":[]}}`); got != "" {
		t.Errorf("SessionID(assistant line) = %q, want empty", got)
	}
	if got := SessionID(`{"type":"system","subtype":"init","session_id":"sess-abc"}`); got != "sess-abc" {
		t.Errorf("SessionID(init line) = %q, want %q", got, "sess-abc")
	}
}

func TestDecodeUsage(t *testing.T) {
	ev, ok := Decode(`{"type":"result","subtype":"success","result":"done","duration_ms":1234,` +
		`"usage":{"input_tokens":3,"output_tokens":7,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}`)
	if !ok {
		t.Fatal("a result line must decode")
	}
	if got, want := ev.Usage.OutputTokens, 7; got != want {
		t.Errorf("OutputTokens = %d, want %d", got, want)
	}
	// The three input lanes are one billed number to a caller.
	if got, want := ev.Usage.Input(), 123; got != want {
		t.Errorf("Input() = %d, want %d (3 plain + 100 cache read + 20 cache write)", got, want)
	}
	if got, want := ev.DurationMS, 1234; got != want {
		t.Errorf("DurationMS = %d, want %d", got, want)
	}
	if ev.Usage.Empty() {
		t.Error("Empty() must be false for a result event that reported usage")
	}

	ev, ok = Decode(`{"type":"assistant","message":{"content":[]}}`)
	if !ok {
		t.Fatal("an assistant line must decode")
	}
	if !ev.Usage.Empty() {
		t.Error("Empty() must be true for an event that reported no usage")
	}
}

func TestDecodeAssistantBlocks(t *testing.T) {
	ev, ok := Decode(`{"type":"assistant","message":{"content":[` +
		`{"type":"text","text":"let me look"},` +
		`{"type":"tool_use","id":"tu-1","name":"Read","input":{"file_path":"/etc/hosts"}}]}}`)
	if !ok {
		t.Fatal("an assistant line must decode")
	}

	blocks := ev.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "let me look" {
		t.Errorf("block 0 = %+v, want the text block", blocks[0])
	}
	if blocks[1].Type != "tool_use" || blocks[1].Name != "Read" {
		t.Errorf("block 1 = %+v, want the tool_use block", blocks[1])
	}
	if got := string(blocks[1].Input); got != `{"file_path":"/etc/hosts"}` {
		t.Errorf("tool input = %s, want the raw input object", got)
	}
	// The id is what pairs this call with the result that answers it, and the wire
	// spells it "id" on the call and "tool_use_id" on the result. A reader that
	// only knows the second spelling loses every tool step, quietly.
	if blocks[1].ToolUseID != "tu-1" {
		t.Errorf("tool_use id = %q, want tu-1", blocks[1].ToolUseID)
	}

	// Blocks() is about messages. Anything else has none, and asking must not
	// panic or invent them.
	if got := (&Event{Type: EventResult, Result: "x"}).Blocks(); got != nil {
		t.Errorf("Blocks() on a result event = %+v, want nil", got)
	}
	if got := (Event{Type: EventAssistant}).Blocks(); got != nil {
		t.Errorf("Blocks() with no message = %+v, want nil", got)
	}
}

// TestDecodeThinkingBlock pins the reasoning block apart from the text block.
//
// The CLI sends the model's chain-of-thought as its own block kind whose payload
// key is "thinking", not "text". A reader that only knows "text" decodes the
// block to nothing and drops the reasoning silently; a reader that folds it into
// Text leaks chain-of-thought into the answer.
func TestDecodeThinkingBlock(t *testing.T) {
	ev, ok := Decode(`{"type":"assistant","message":{"content":[` +
		`{"type":"thinking","thinking":"先看十分位","signature":"sig"},` +
		`{"type":"text","text":"9.9 更大"}]}}`)
	if !ok {
		t.Fatal("an assistant line must decode")
	}

	blocks := ev.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].Type != "thinking" || blocks[0].Thinking != "先看十分位" {
		t.Errorf("block 0 = %+v, want the thinking block with its text", blocks[0])
	}
	if blocks[0].Text != "" {
		t.Errorf("thinking text leaked into Text: %q", blocks[0].Text)
	}
	if blocks[1].Type != "text" || blocks[1].Text != "9.9 更大" || blocks[1].Thinking != "" {
		t.Errorf("block 1 = %+v, want the text block alone", blocks[1])
	}
}

// TestFinalTextIgnoresThinking is the leak guard on the answer path: FinalText
// is the answer of record, so reasoning must never reach it however the CLI
// frames the turn. This is the fallback the interrupted-turn path uses, where
// there is no result event to prefer over the assistant's own blocks.
func TestFinalTextIgnoresThinking(t *testing.T) {
	events := []Event{
		{Type: EventAssistant, Message: json.RawMessage(`{"content":[` +
			`{"type":"thinking","thinking":"先看十分位","signature":"sig"},` +
			`{"type":"text","text":"9.9 更大"}]}`)},
	}
	if got, want := FinalText(events), "9.9 更大"; got != want {
		t.Errorf("FinalText = %q, want %q (reasoning excluded)", got, want)
	}

	// A turn that thought but never answered must read as empty, not as the
	// chain-of-thought.
	thinkingOnly := []Event{
		{Type: EventAssistant, Message: json.RawMessage(`{"content":[` +
			`{"type":"thinking","thinking":"先看十分位"}]}`)},
	}
	if got := FinalText(thinkingOnly); got != "" {
		t.Errorf("FinalText = %q, want empty for a reasoning-only turn", got)
	}
}

func TestDecodeToolResult(t *testing.T) {
	// The string form: what most tools return.
	ev, ok := Decode(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu-1",` +
		`"content":"127.0.0.1 localhost","is_error":false}]}}`)
	if !ok {
		t.Fatal("a user line carrying a tool_result must decode")
	}
	blocks := ev.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	if blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "tu-1" {
		t.Errorf("block = %+v, want a tool_result tied to tu-1", blocks[0])
	}
	if blocks[0].Text != "127.0.0.1 localhost" {
		t.Errorf("Text = %q, want the tool output", blocks[0].Text)
	}
	if blocks[0].IsError {
		t.Error("IsError must follow the block's own flag")
	}

	// The array form: a tool result that carries an attachment. Reading only the
	// string form renders these as empty, which looks like a tool that returned
	// nothing rather than a decode that dropped the answer.
	ev, ok = Decode(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu-2",` +
		`"is_error":true,"content":[{"type":"text","text":"first"},{"type":"image","source":{}},` +
		`{"type":"text","text":"second"}]}]}}`)
	if !ok {
		t.Fatal("a tool_result with array content must decode")
	}
	blocks = ev.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	if got, want := blocks[0].Text, "first\nsecond"; got != want {
		t.Errorf("Text = %q, want %q (text parts joined, non-text parts skipped)", got, want)
	}
	if !blocks[0].IsError {
		t.Error("IsError must follow the block's own flag")
	}
}

func TestDecodeLargeToolResult(t *testing.T) {
	// A single tool_result is one line, and reading a large file or a
	// `kubectl get -o json` reaches megabytes. The scanner limit must be far
	// above that: exceeding it abandons the stream and kills the session.
	const payload = 256 << 10
	line := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu-1","content":"` +
		strings.Repeat("x", payload) + `"}]}}`
	if len(line) <= 64*1024 {
		t.Fatalf("the fixture is %d bytes; it has to exceed the scanner's start size to be a test", len(line))
	}

	var got []Event
	if err := readStream(strings.NewReader(line+"\n"), MaxLineBytes, nil, func(ev Event) { got = append(got, ev) }); err != nil {
		t.Fatalf("a %d-byte line must be read, not rejected: %v", len(line), err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	blocks := got[0].Blocks()
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	if len(blocks[0].Text) != payload {
		t.Errorf("the tool result arrived truncated: %d bytes, want %d", len(blocks[0].Text), payload)
	}
}

func TestLineOverTheLimitFailsTheStream(t *testing.T) {
	// The limit is enforced, and enforced as an error. A scanner that truncated
	// instead would leave the rest of the line to be read as a line of its own,
	// and a session that reports nonsense is worse than one that reports a limit.
	long := `{"type":"assistant","message":{"content":[{"type":"text","text":"` +
		strings.Repeat("x", 4096) + `"}]}}`

	// A caller's limit is the limit, even when it is far below the default: the
	// scanner only consults it when the buffer has to grow, so a start size above
	// it would quietly accept the very lines it was set to refuse.
	err := readStream(strings.NewReader(long+"\n"), 1024, nil, func(Event) {})
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("err = %v, want bufio.ErrTooLong", err)
	}

	// The same line passes under a limit that can hold it, so the failure above
	// is the limit and not the fixture.
	var got []Event
	if err := readStream(strings.NewReader(long+"\n"), MaxLineBytes, nil, func(ev Event) { got = append(got, ev) }); err != nil {
		t.Fatalf("readStream under the default limit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
}

func TestOnLineSeesEveryLineAsWritten(t *testing.T) {
	// A transcript is read by a human debugging a run, so it records what the
	// agent wrote — including the lines this package throws away. A transcript
	// rebuilt from decoded events would silently lose them.
	lines := []string{
		`{"type":"telemetry","payload":{"ms":12}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`not json`,
	}
	var seen []string
	var events int
	err := readStream(strings.NewReader(strings.Join(lines, "\n")+"\n"), MaxLineBytes,
		func(line string) { seen = append(seen, line) },
		func(Event) { events++ })
	if err != nil {
		t.Fatalf("readStream: %v", err)
	}
	if len(seen) != len(lines) {
		t.Fatalf("OnLine saw %d lines, want %d: %q", len(seen), len(lines), seen)
	}
	for i := range lines {
		if seen[i] != lines[i] {
			t.Errorf("line %d = %q, want the bytes the agent wrote (%q)", i, seen[i], lines[i])
		}
	}
	// The lines are passed through, not acted on: only one of the three decodes.
	if events != 1 {
		t.Errorf("decoded %d events, want 1", events)
	}
}

func TestDefaultMaxLineBytes(t *testing.T) {
	if MaxLineBytes != 8<<20 {
		t.Errorf("MaxLineBytes = %d, want %d", MaxLineBytes, 8<<20)
	}
	opts := Options{}
	if err := opts.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if opts.MaxLineBytes != MaxLineBytes {
		t.Errorf("a zero Options must default its line limit to %d, got %d", MaxLineBytes, opts.MaxLineBytes)
	}
}

func TestDecodeUnknownLineIsIgnored(t *testing.T) {
	for _, line := range []string{
		``,
		`   `,
		`not json at all`,
		`{"type":"telemetry","payload":{}}`,
		`{"type":"system","subtype":"hook_started"}`,                                     // a system line that is not init
		`{"type":"control_request","request_id":"r1","request":{"subtype":"interrupt"}}`, // not ours to answer
		`{"type":"control_response","response":{"subtype":"success"}}`,
	} {
		if ev, ok := Decode(line); ok {
			t.Errorf("Decode(%q) decoded to %+v, want it ignored", line, ev)
		}
	}
}

func TestDecodeControlRequestCanUseTool(t *testing.T) {
	ev, ok := Decode(`{"type":"control_request","request_id":"req-7","request":{"subtype":"can_use_tool",` +
		`"tool_name":"Bash","input":{"command":"rm -rf /tmp/x"},"tool_use_id":"tu-9"}}`)
	if !ok {
		t.Fatal("a can_use_tool request must decode: it is the one line the agent blocks on")
	}
	if ev.Control == nil {
		t.Fatal("Control must be set on a permission request")
	}
	if got, want := ev.Control.RequestID, "req-7"; got != want {
		t.Errorf("RequestID = %q, want %q", got, want)
	}
	if got, want := ev.Control.ToolName, "Bash"; got != want {
		t.Errorf("ToolName = %q, want %q", got, want)
	}
	if got, want := string(ev.Control.Input), `{"command":"rm -rf /tmp/x"}`; got != want {
		t.Errorf("Input = %s, want %s", got, want)
	}
	if got, want := ev.Control.ToolUseID, "tu-9"; got != want {
		t.Errorf("ToolUseID = %q, want %q", got, want)
	}

	// Other event kinds must not carry a control request.
	if ev, _ := Decode(`{"type":"assistant","message":{"content":[]}}`); ev.Control != nil {
		t.Errorf("Control = %+v on an assistant event, want nil", ev.Control)
	}
}

func TestFinalTextPrefersResultEvent(t *testing.T) {
	events := []Event{
		{Type: EventSystem, Subtype: SubtypeInit, SessionID: "s"},
		{Type: EventAssistant, Message: json.RawMessage(`{"content":[{"type":"text","text":"draft"}]}`)},
		{Type: EventResult, Result: "the answer"},
	}
	if got, want := FinalText(events), "the answer"; got != want {
		t.Errorf("FinalText = %q, want %q", got, want)
	}

	// An interrupted turn ends without a result event. What the assistant had
	// already said is still the answer to show.
	events = []Event{
		{Type: EventAssistant, Message: json.RawMessage(`{"content":[{"type":"text","text":"part one"}]}`)},
		{Type: EventAssistant, Message: json.RawMessage(`{"content":[` +
			`{"type":"tool_use","name":"Read","input":{}},` +
			`{"type":"text","text":"part two"}]}`)},
		{Type: EventUser, Message: json.RawMessage(`{"content":[{"type":"tool_result","content":"ignored"}]}`)},
	}
	if got, want := FinalText(events), "part one\npart two"; got != want {
		t.Errorf("FinalText = %q, want %q (assistant text only, joined)", got, want)
	}

	if got := FinalText(nil); got != "" {
		t.Errorf("FinalText(nil) = %q, want empty", got)
	}
}
