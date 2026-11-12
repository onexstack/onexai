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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agent "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

func eventStream(events ...agent.Event) <-chan agent.Event {
	out := make(chan agent.Event, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out
}

func TestCollectReturnsTheAnswer(t *testing.T) {
	result, err := collect(context.Background(), "task-1", "session-1", eventStream(
		agent.Event{Type: agent.EventAnswer, Delta: "partial "},
		agent.Event{Type: agent.EventDone, Final: "the whole answer"},
	))
	require.NoError(t, err)
	assert.Equal(t, "completed", result.State)
	assert.Equal(t, "the whole answer", result.Text)
	assert.Equal(t, "task-1", result.TaskID)
	assert.Equal(t, "session-1", result.Session)
}

func TestCollectFallsBackToTheStreamedText(t *testing.T) {
	result, err := collect(context.Background(), "task-1", "", eventStream(
		agent.Event{Type: agent.EventAnswer, Delta: "streamed "},
		agent.Event{Type: agent.EventAnswer, Delta: "answer"},
		agent.Event{Type: agent.EventDone},
	))
	require.NoError(t, err)
	assert.Equal(t, "streamed answer", result.Text)
}

func TestCollectReportsAPause(t *testing.T) {
	result, err := collect(context.Background(), "task-1", "", eventStream(
		agent.Event{Type: agent.EventInterrupted, Interrupt: &agent.Interrupt{
			RequestID: "req-1",
			Requests: []agent.InterruptRequest{
				{Kind: "permission", ToolName: "delete_pod"},
				{Kind: "question", Question: "which namespace?"},
			},
		}},
	))
	require.NoError(t, err)
	assert.Equal(t, "input-required", result.State)
	require.Len(t, result.Interrupts, 2)
	assert.Equal(t, "req-1", result.Interrupts[0].RequestID)
	assert.Equal(t, "delete_pod", result.Interrupts[0].ToolName)
	assert.Equal(t, "which namespace?", result.Interrupts[1].Question)
}

func TestCollectReportsFailure(t *testing.T) {
	result, err := collect(context.Background(), "task-1", "", eventStream(
		agent.Event{Type: agent.EventFailed, Err: assert.AnError},
	))
	require.NoError(t, err)
	assert.Equal(t, "failed", result.State)
	assert.Equal(t, assert.AnError.Error(), result.Error)
}

// A stream that ends without a terminal event is a cut-off run, not a success:
// reporting "completed" with no answer would tell the caller the work was done.
func TestCollectRefusesATruncatedStream(t *testing.T) {
	_, err := collect(context.Background(), "task-1", "", eventStream(
		agent.Event{Type: agent.EventAnswer, Delta: "half an ans"},
	))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal event")
}

func TestDelegateSessionIsStableAndScopedToTheParent(t *testing.T) {
	assert.Equal(t, "parent/delegate/researcher", delegateSession("parent", "researcher"))
	assert.Contains(t, delegateSession("", "researcher"), "delegate-")
	assert.NotEqual(t, delegateSession("", "researcher"), delegateSession("", "researcher"))
}

func TestDelegationForRequiresDeclaredDelegates(t *testing.T) {
	sel := resolvedSelection{spec: RunSpec{}}
	assert.Nil(t, delegationFor(&agent.AskRequest{}, sel))

	sel.spec.Delegates = []string{"researcher"}
	delegation := delegationFor(&agent.AskRequest{
		SessionID: "session-1",
		Subject:   agent.Principal{ID: "u1", Kind: agent.PrincipalKindUser},
		Lineage:   &agent.Lineage{RootRunID: "root-1", Depth: 1},
	}, sel)
	require.NotNil(t, delegation)
	assert.Equal(t, []string{"researcher"}, delegation.Agents)
	assert.Equal(t, "session-1", delegation.ParentSession)
	assert.Equal(t, "u1", delegation.Subject)
	assert.Equal(t, "root-1", delegation.RootRunID)
	assert.Equal(t, 1, delegation.Depth)
}

func TestResumeDelegationKeepsTheChain(t *testing.T) {
	sel := resolvedSelection{spec: RunSpec{Delegates: []string{"researcher"}}}
	delegation := resumeDelegation(agent.RunMeta{
		SessionID:   "session-1",
		UserID:      "u1",
		SubjectKind: agent.PrincipalKindService,
		Lineage:     &agent.Lineage{RootRunID: "root-1", Depth: 2},
	}, sel)
	require.NotNil(t, delegation)
	assert.Equal(t, "u1", delegation.Subject)
	assert.Equal(t, agent.PrincipalKindService, delegation.SubjectKind)
	assert.Equal(t, "root-1", delegation.RootRunID)
	assert.Equal(t, 2, delegation.Depth)
}

// TestInlineDocumentsLeavesANoteForAnUnreadableFile pins that a document which
// cannot be read still tells the model something was attached: dropping it
// silently produces a confident answer about a file the model never saw.
func TestInlineDocumentsLeavesANoteForAnUnreadableFile(t *testing.T) {
	biz := &AgentBiz{}
	question := biz.inlineDocuments(context.Background(), "what is in the file?", []media.Ref{{
		Name: "spec.md", MimeType: "text/markdown", URL: "asset://missing",
	}})

	assert.Contains(t, question, "what is in the file?")
	assert.Contains(t, question, `<document name="spec.md">could not be read</document>`)
}

func TestInlineDocumentsWithoutDocumentsIsANoOp(t *testing.T) {
	biz := &AgentBiz{}
	assert.Equal(t, "unchanged", biz.inlineDocuments(context.Background(), "unchanged", nil))
}
