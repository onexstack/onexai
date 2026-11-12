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

// Package agentcli is the shared runtime for driving a local Claude Code process
// (`claude --output-format stream-json`) as an agent session. It owns the process
// lifecycle, the stream-json codec and the permission handshake, and nothing
// else: it renders nothing, imports nothing from either service tree, and is
// deliberately free of any knowledge of Lark, the TUI or this repository's API
// types, so the private harness CLI and the server (internal/chat) can
// both build on it.
package agentcli

import "encoding/json"

// The line types the CLI emits on stdout with --output-format stream-json. The
// control types are the permission handshake; see Runner.RespondPermission.
const (
	EventSystem         = "system"
	EventAssistant      = "assistant"
	EventUser           = "user"
	EventResult         = "result"
	EventControlRequest = "control_request"
)

// Subtypes this package gives meaning to. They are the CLI's own vocabulary, not
// ours: "init" carries the session to resume, and "can_use_tool" is the one
// control request the host has to answer for --permission-prompt-tool stdio to
// work at all.
const (
	SubtypeInit       = "init"
	SubtypeCanUseTool = "can_use_tool"
)

// Event is one decoded stream-json line.
//
// It is a plain struct rather than a protobuf on purpose: the events are the
// CLI's wire format, which changes with the CLI, and a generated type would put
// a code-generation step between this repository and a format it does not own.
type Event struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	// SessionID is set on the init event and is what --resume takes.
	SessionID string `json:"session_id,omitempty"`
	// Message is the assistant or user message payload, decoded by Blocks.
	Message json.RawMessage `json:"message,omitempty"`
	// Result is the final answer text on a result event.
	Result string `json:"result,omitempty"`
	// Usage is the token accounting on a result event.
	Usage Usage `json:"usage"`
	// DurationMS is the wall-clock duration reported on a result event.
	DurationMS int `json:"duration_ms,omitempty"`
	// IsError marks a result event that reports a failed turn.
	IsError bool `json:"is_error,omitempty"`
	// Control is the permission request when the agent has stopped to ask for
	// one. It is nil on every other event.
	Control *PermissionRequest `json:"-"`
}

// Usage is the token accounting a result event reports.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// Input returns the input tokens the turn is billed for, which is the three
// input lanes the CLI reports summed into one number.
func (u Usage) Input() int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// Empty reports whether the event carried no usage at all.
func (u Usage) Empty() bool {
	return u == Usage{}
}

// Block is one content block of an assistant or user message.
type Block struct {
	// Type is "text", "tool_use", "tool_result", "thinking" or another CLI
	// block kind.
	Type string
	// Text is the block's text, for "text" and "tool_result" blocks.
	//
	// A "thinking" block's text is deliberately NOT here: it is the model's
	// reasoning, and the answer path (see FinalText) reads Text to decide what
	// the assistant said. Folding the two together would leak chain-of-thought
	// into the answer.
	Text string
	// Thinking is the model's reasoning, for "thinking" blocks.
	Thinking string
	// Name is the tool name, for "tool_use" blocks.
	Name string
	// Input is the raw JSON tool input, for "tool_use" blocks.
	Input json.RawMessage
	// ToolUseID ties a "tool_result" back to its "tool_use".
	ToolUseID string
	// IsError marks a "tool_result" that reports a failure.
	IsError bool
}

// Blocks decodes the content of an assistant or user message. It returns nil for
// any other event, and for a payload it cannot decode — an unknown line is not
// an error, since the CLI may grow new block kinds at any time.
func (e Event) Blocks() []Block {
	if len(e.Message) == 0 || (e.Type != EventAssistant && e.Type != EventUser) {
		return nil
	}

	var message struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Name      string          `json:"name"`
			ID        string          `json:"id"`
			Input     json.RawMessage `json:"input"`
			Content   json.RawMessage `json:"content"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
		} `json:"content"`
	}
	if err := json.Unmarshal(e.Message, &message); err != nil {
		return nil
	}

	blocks := make([]Block, 0, len(message.Content))
	for _, c := range message.Content {
		blocks = append(blocks, Block{
			Type:      c.Type,
			Text:      blockText(c.Text, c.Content),
			Thinking:  c.Thinking,
			Name:      c.Name,
			Input:     c.Input,
			ToolUseID: toolUseID(c.ToolUseID, c.ID),
			IsError:   c.IsError,
		})
	}
	return blocks
}

// toolUseID returns the call id a block carries. The wire spells it two ways: a
// "tool_use" block names itself with "id", and the "tool_result" answering it
// points back with "tool_use_id". Reading only one of the two unlinks every call
// from its result, which is not a visible error — the result is simply dropped
// as one that belongs to a call nobody saw.
func toolUseID(toolUseID, id string) string {
	if toolUseID != "" {
		return toolUseID
	}
	return id
}

// blockText returns a block's text, which a tool_result may carry either as a
// plain string or as an array of text blocks. The array form is not a rarity —
// it is what a tool result containing an attachment looks like — and reading
// only the string form silently renders those results as empty.
func blockText(text string, content json.RawMessage) string {
	if text != "" {
		return text
	}
	if len(content) == 0 {
		return ""
	}

	var plain string
	if err := json.Unmarshal(content, &plain); err == nil {
		return plain
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	var joined string
	for _, p := range parts {
		if p.Type != "text" || p.Text == "" {
			continue
		}
		if joined != "" {
			joined += "\n"
		}
		joined += p.Text
	}
	return joined
}
