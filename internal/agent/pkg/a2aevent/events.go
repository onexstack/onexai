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

// Package a2aevent builds the A2A status events and reads message text shared by
// the gateway executor and the runtime executors.
//
// It depends only on the A2A protocol types and the shared wire contract, so
// both the gateway and a runtime binary can import it without pulling either
// side in.
package a2aevent

import (
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/onexstack/onexai/internal/agent/pkg/a2awire"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// Status returns a status update, with a message when there is one to give.
func Status(execCtx *a2asrv.ExecutorContext, state a2a.TaskState, text string) *a2a.TaskStatusUpdateEvent {
	if text == "" {
		return a2a.NewStatusUpdateEvent(execCtx, state, nil)
	}
	return a2a.NewStatusUpdateEvent(execCtx, state, &a2a.Message{
		ID:        a2a.NewMessageID(),
		Role:      a2a.MessageRoleAgent,
		Parts:     []*a2a.Part{a2a.NewTextPart(text)},
		ContextID: execCtx.ContextID,
		TaskID:    execCtx.TaskID,
	})
}

// StatusWithUsage attaches the task-lifetime token usage to a terminal status.
func StatusWithUsage(
	execCtx *a2asrv.ExecutorContext, state a2a.TaskState, text string, usage *a2awire.Usage,
) *a2a.TaskStatusUpdateEvent {
	event := Status(execCtx, state, text)
	if usage == nil {
		return event
	}
	event.Metadata = usage.Metadata()
	return event
}

// UsageFromAgentAPI converts the domain usage to the wire shape. It returns nil
// for nil input so a run that reports nothing produces the plain status event.
func UsageFromAgentAPI(usage *agentapi.Usage) *a2awire.Usage {
	if usage == nil {
		return nil
	}
	return &a2awire.Usage{
		InputTokens:       usage.PromptTokens,
		OutputTokens:      usage.CompletionTokens,
		ReasoningTokens:   usage.ReasoningTokens,
		CachedInputTokens: usage.CacheHitTokens,
		TotalTokens:       usage.TotalTokens,
	}
}

// MessageText concatenates a message's text parts in order.
func MessageText(message *a2a.Message) string {
	if message == nil {
		return ""
	}
	var b strings.Builder
	for _, part := range message.Parts {
		if part == nil {
			continue
		}
		if text, ok := part.Content.(a2a.Text); ok {
			b.WriteString(string(text))
		}
	}
	return b.String()
}

// UserText returns the text of the request's message, concatenated in part
// order.
func UserText(req *a2a.SendMessageRequest) string {
	if req == nil {
		return ""
	}
	return MessageText(req.Message)
}
