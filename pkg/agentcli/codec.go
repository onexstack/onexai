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
	"io"
	"strings"
)

// readStream decodes stream-json lines from r until EOF, handing each event to
// emit as it arrives. Lines it does not recognise are skipped, not reported, and
// passed to onLine when there is one — a transcript records what the agent said,
// including the lines this package has no use for.
//
// maxLineBytes is a hard limit on one line, and exceeding it is an error rather
// than a truncation: a scanner that stops mid-line would leave the rest of that
// line to be read as a line of its own, and a session that reports nonsense is
// worse than one that reports the limit it hit.
func readStream(r io.Reader, maxLineBytes int, onLine func(string), emit func(Event)) error {
	if maxLineBytes <= 0 {
		maxLineBytes = MaxLineBytes
	}
	// Start small and let the scanner grow: most events are a few hundred bytes,
	// and only a tool_result for a large file reaches megabytes. The start size
	// is capped by the limit, because the limit is only consulted when the
	// buffer has to grow — a start size above it would silently accept lines
	// larger than the caller asked for.
	start := min(64*1024, maxLineBytes)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, start), maxLineBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if onLine != nil {
			onLine(line)
		}
		if ev, ok := Decode(line); ok {
			emit(ev)
		}
	}
	return scanner.Err()
}

// Decode decodes one line of stream-json output.
//
// It reports false for a line it does not recognise rather than failing: the
// stream carries line types this package has no use for (progress, telemetry,
// the host's own control_responses echoed back), and a CLI that grows a new one
// must not turn into a broken session.
func Decode(line string) (Event, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Event{}, false
	}

	var w wire
	if err := json.Unmarshal([]byte(line), &w); err != nil {
		return Event{}, false
	}
	ev := w.Event

	switch ev.Type {
	case EventControlRequest:
		// Only a tool-approval request blocks the agent on us; the other control
		// requests either have no answer or are not ours to give.
		if w.Request.Subtype != SubtypeCanUseTool {
			return Event{}, false
		}
		ev.Control = &PermissionRequest{
			RequestID: w.RequestID,
			ToolName:  w.Request.ToolName,
			Input:     w.Request.Input,
			ToolUseID: w.Request.ToolUseID,
		}
	case EventSystem:
		// Only the init event is meaningful, and only for its session ID.
		if ev.Subtype != SubtypeInit {
			return Event{}, false
		}
	case EventAssistant, EventUser, EventResult:
	default:
		return Event{}, false
	}
	return ev, true
}

// wire is the union of every field this package reads from one line. The control
// fields sit alongside the message fields rather than in a nested struct because
// that is how they arrive: one JSON object per line, flat.
type wire struct {
	Event
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype   string          `json:"subtype"`
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	} `json:"request"`
}

// SessionID returns the session ID an init line reports, or "" for any other
// line. It is the ID --resume takes, and it exists on the init event alone:
// reading it from every line that happens to carry a session_id accepts a
// session that a later event has already moved on from.
func SessionID(line string) string {
	ev, ok := Decode(line)
	if !ok || ev.Type != EventSystem {
		return ""
	}
	return ev.SessionID
}

// FinalText returns the answer a completed stream carries: the result event's own
// text when there is one, and otherwise the assistant's text blocks joined.
//
// The fallback matters for interrupted turns, which end without a result event —
// what the assistant had already said is still the answer to show.
func FinalText(events []Event) string {
	var result string
	var assistant []string
	for _, ev := range events {
		switch ev.Type {
		case EventResult:
			if strings.TrimSpace(ev.Result) != "" {
				result = ev.Result
			}
		case EventAssistant:
			for _, b := range ev.Blocks() {
				if b.Type == "text" && b.Text != "" {
					assistant = append(assistant, b.Text)
				}
			}
		}
	}
	if strings.TrimSpace(result) != "" {
		return result
	}
	return strings.Join(assistant, "\n")
}
