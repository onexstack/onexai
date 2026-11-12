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

package oncallsummary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// Timeout bounds one generation.
//
// It is set here rather than left to the caller because the caller is a chat
// command: without a bound, a model that never answers holds the goroutine open
// for the life of the process, and the ticket it was summarizing stays
// unsummarized with nothing to say why. Three minutes is generous for a document
// this size — the client's own configured timeout is shorter, and its value wins
// when it is.
const Timeout = 3 * time.Minute

// Completer is the one method this package needs from the agent client.
//
// A consumer-side interface rather than the concrete client, for the reason the
// repository uses this shape elsewhere: the method can then be driven by a fake in
// a test that is about the prompt and the parsing, without an HTTP server.
type Completer interface {
	ChatCompletions(ctx context.Context, req *agentv1.ChatCompletionRequest) (*agentv1.ChatCompletionResponse, error)
}

// Generate runs one non-streaming completion and returns its text.
//
// # Why this is a completion and not an agent run
//
// The bot's other model path is a streaming conversation with tools, approvals and
// a card the user watches. None of that applies here: there is no user waiting on
// a stream, no tool the summary needs, and an approval pause would be an answer
// nobody could give on a background goroutine. `ChatCompletions` is the
// non-streaming door to the same service.
//
// # Why the request pins NoAgent
//
// The far side requires an agent — an empty one is a validation error — and its
// `model` field names a model endpoint rather than an agent. NoAgent is the
// explicit request for the builtin assistant, which holds no tools, no knowledge
// bases and no instruction of its own, so this stays a one-shot completion whose
// whole shape is the prompt below. Skipping the field would not do: it would be
// refused, not defaulted.
//
// # Why the prompt is one message
//
// The far side reads the **last** message with role `user` as the question and
// ignores everything before it. Sending a system message plus a user message would
// therefore drop the system message silently — the instruction would simply not
// happen, and the answer would look like a model that ignored it. One message, so
// there is nothing to drop.
func Generate(ctx context.Context, c Completer, sessionID, prompt string) (string, error) {
	if c == nil {
		return "", errors.New("oncall-summary: no agent client is wired in")
	}
	if strings.TrimSpace(prompt) == "" {
		// An empty prompt asks the model to write about nothing, and it will.
		return "", errors.New("oncall-summary: the prompt is empty")
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	resp, err := c.ChatCompletions(ctx, &agentv1.ChatCompletionRequest{
		// The caller's, and it must be per **ticket**: the far side keys its
		// conversation memory by this value, so two tickets sharing one would let
		// one incident's material into the other's summary. Reusing a key across a
		// ticket's own retries is fine — and better than a fresh key per call, which
		// would leave one session behind upstream for every summary ever written.
		SessionID: sessionID,
		Agent:     agentv1.NoAgent,
		Messages: []*agentv1.ChatCompletionMessage{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("oncall-summary: the model call failed: %w", err)
	}

	for _, choice := range resp.GetChoices() {
		if message := choice.GetMessage(); message != nil {
			if text := strings.TrimSpace(message.GetContent()); text != "" {
				return text, nil
			}
		}
	}
	// A 200 with no text is not a summary. Refused rather than stored, because
	// storing it would leave the ticket looking summarized while its summary is
	// empty — and every reader of that field treats empty as "not generated yet",
	// which is the one thing it would then not be.
	return "", errors.New("oncall-summary: the model answered without any content")
}
