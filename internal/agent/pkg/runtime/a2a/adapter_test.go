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

package a2a

import (
	"testing"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agent2a "github.com/onexstack/onexai/internal/agent/pkg/a2a"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

func TestArtifactTextAndCompletion(t *testing.T) {
	state := &streamState{}
	events := state.translate(&a2atype.TaskArtifactUpdateEvent{
		Artifact: &a2atype.Artifact{Parts: []*a2atype.Part{a2atype.NewTextPart("hel")}},
	})
	require.Len(t, events, 1)
	assert.Equal(t, agentapi.EventAnswer, events[0].Type)
	assert.Equal(t, "hel", events[0].Delta)

	events = state.translate(&a2atype.TaskArtifactUpdateEvent{
		Artifact: &a2atype.Artifact{Parts: []*a2atype.Part{a2atype.NewTextPart("lo")}},
	})
	require.Len(t, events, 1)

	done := state.translate(&a2atype.TaskStatusUpdateEvent{Status: a2atype.TaskStatus{State: a2atype.TaskStateCompleted}})
	require.Len(t, done, 1)
	assert.Equal(t, agentapi.EventDone, done[0].Type)
	assert.Equal(t, "hello", done[0].Final)
}

func TestReasoningPart(t *testing.T) {
	part := a2atype.NewTextPart("thinking")
	part.Metadata = map[string]any{agent2a.ThoughtMetadataKey: true}
	events := (&streamState{}).translate(&a2atype.TaskArtifactUpdateEvent{
		Artifact: &a2atype.Artifact{Parts: []*a2atype.Part{part}},
	})
	require.Len(t, events, 1)
	assert.Equal(t, agentapi.EventReasoning, events[0].Type)
	assert.Equal(t, "thinking", events[0].Delta)
}

func TestToolDataPart(t *testing.T) {
	part := a2atype.NewDataPart(map[string]any{
		"id":         "call-1",
		"name":       "deploy",
		"status":     "ok",
		"durationMs": float64(12),
		"result":     "done",
	})
	part.Metadata = map[string]any{agent2a.PartTypeMetadataKey: "tool"}
	events := (&streamState{}).translate(&a2atype.TaskArtifactUpdateEvent{
		Artifact: &a2atype.Artifact{Parts: []*a2atype.Part{part}},
	})
	require.Len(t, events, 1)
	assert.Equal(t, agentapi.EventToolStep, events[0].Type)
	require.Len(t, events[0].Steps, 1)
	assert.Equal(t, "deploy", events[0].Steps[0].Name)
	assert.Equal(t, "ok", events[0].Steps[0].Status)
	assert.Equal(t, "done", events[0].Steps[0].Result)
}

// TestStructuredDataPartIsTheAnswer covers a peer that publishes only the
// data form: the gateway still needs the text to validate and to hand back.
func TestStructuredDataPartIsTheAnswer(t *testing.T) {
	part := a2atype.NewDataPart(map[string]any{"severity": "high"})
	part.Metadata = map[string]any{agent2a.PartTypeMetadataKey: "structured-output"}
	state := &streamState{}
	events := state.translate(&a2atype.TaskArtifactUpdateEvent{
		Artifact: &a2atype.Artifact{Parts: []*a2atype.Part{part}},
	})
	require.Len(t, events, 1)
	assert.Equal(t, agentapi.EventAnswer, events[0].Type)
	assert.JSONEq(t, `{"severity":"high"}`, events[0].Delta)

	done := state.translate(&a2atype.TaskStatusUpdateEvent{Status: a2atype.TaskStatus{State: a2atype.TaskStateCompleted}})
	require.Len(t, done, 1)
	assert.JSONEq(t, `{"severity":"high"}`, done[0].Final)
}

// TestStructuredAnswerIsNotCountedTwice covers a peer that sends both forms,
// which is what this service's own executor does.
func TestStructuredAnswerIsNotCountedTwice(t *testing.T) {
	data := a2atype.NewDataPart(map[string]any{"severity": "high"})
	data.Metadata = map[string]any{agent2a.PartTypeMetadataKey: "structured-output"}
	state := &streamState{}
	events := state.translate(&a2atype.TaskArtifactUpdateEvent{
		Artifact: &a2atype.Artifact{Parts: []*a2atype.Part{data, a2atype.NewTextPart(`{"severity":"high"}`)}},
	})
	require.Len(t, events, 1, "only the text part should produce an answer event")

	done := state.translate(&a2atype.TaskStatusUpdateEvent{Status: a2atype.TaskStatus{State: a2atype.TaskStateCompleted}})
	require.Len(t, done, 1)
	assert.JSONEq(t, `{"severity":"high"}`, done[0].Final)
}

func TestInputRequiredBecomesInterrupt(t *testing.T) {
	message := &a2atype.Message{
		ID:    "pause-1",
		Role:  a2atype.MessageRoleAgent,
		Parts: []*a2atype.Part{a2atype.NewTextPart("approve?")},
		Metadata: map[string]any{
			agent2a.HITLExtensionURI: map[string]any{
				"type": agent2a.HITLTypeToolApprovalRequest,
				"id":   "pause-1",
				"hint": "Allow these calls?",
				"tools": []any{
					map[string]any{"id": "t1", "name": "delete_file", "args": map[string]any{"path": "/x"}},
				},
			},
		},
	}
	events := (&streamState{}).translate(&a2atype.TaskStatusUpdateEvent{
		Status: a2atype.TaskStatus{State: a2atype.TaskStateInputRequired, Message: message},
	})
	require.Len(t, events, 1)
	assert.Equal(t, agentapi.EventInterrupted, events[0].Type)
	require.NotNil(t, events[0].Interrupt)
	assert.Equal(t, "pause-1", events[0].Interrupt.RequestID)
	require.Len(t, events[0].Interrupt.Requests, 1)
	assert.Equal(t, "delete_file", events[0].Interrupt.Requests[0].ToolName)
	assert.Contains(t, events[0].Interrupt.Requests[0].Arguments, "/x")
}

func TestUsageAndCancel(t *testing.T) {
	state := &streamState{}
	state.translate(&a2atype.TaskStatusUpdateEvent{Metadata: map[string]any{
		agent2a.UsageExtensionURI: map[string]any{
			"inputTokens":  float64(10),
			"outputTokens": float64(5),
			"totalTokens":  float64(15),
		},
	}})
	events := state.translate(&a2atype.TaskStatusUpdateEvent{Status: a2atype.TaskStatus{State: a2atype.TaskStateCanceled}})
	require.Len(t, events, 1)
	assert.Equal(t, agentapi.EventStopped, events[0].Type)
}

func TestFailedStatusCarriesText(t *testing.T) {
	events := (&streamState{}).translate(&a2atype.TaskStatusUpdateEvent{
		Status: a2atype.TaskStatus{
			State:   a2atype.TaskStateFailed,
			Message: &a2atype.Message{ID: "m", Role: a2atype.MessageRoleAgent, Parts: []*a2atype.Part{a2atype.NewTextPart("boom")}},
		},
	})
	require.Len(t, events, 1)
	assert.Equal(t, agentapi.EventFailed, events[0].Type)
	require.Error(t, events[0].Err)
	assert.Contains(t, events[0].Err.Error(), "boom")
}
