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
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// scanSSELines reads a text/event-stream body and invokes onChunk for each
// "data: {json}" payload. It stops at the "[DONE]" sentinel and returns the
// first onChunk error, or any scanner error.
func scanSSELines(body io.Reader, onChunk func(*v1.ChatCompletionStreamResponse) error) error {
	scanner := bufio.NewScanner(body)
	// A single SSE chunk is small; raise the default token limit defensively.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	sawDone := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			sawDone = true
			return nil
		}
		var chunk v1.ChatCompletionStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		// A terminal chunk with finish_reason "error" signals a failed run;
		// surface it so callers don't mistake it for a clean end.
		for _, choice := range chunk.GetChoices() {
			if choice.GetFinishReason() == "error" {
				return errors.New("chat completion failed")
			}
		}
		if err := onChunk(&chunk); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !sawDone {
		return errors.New("chat completion stream ended unexpectedly")
	}
	return nil
}

// scanAgentSSE reads the /v1/agent/ask event stream and invokes onEvent for each
// AgentStreamEvent. The stream ends when the server closes the response body
// (after the terminal done/failed/stopped event), so EOF is a clean end.
func scanAgentSSE(body io.Reader, onEvent func(*v1.AgentStreamEvent) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return nil
		}
		var ev v1.AgentStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		if err := onEvent(&ev); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		// A body that stops mid-stream is what a severed SSE connection looks
		// like: the server's write deadline fired, or it went away. Returning the
		// raw io.ErrUnexpectedEOF reaches the caller as the bare and mysterious
		// "unexpected EOF", with nothing to say it came from the transport rather
		// than from the run. The onEvent error above is a different thing — the
		// backend reporting a failed run — so it stays unwrapped.
		return fmt.Errorf("agent agent stream truncated: %w", err)
	}
	return nil
}
