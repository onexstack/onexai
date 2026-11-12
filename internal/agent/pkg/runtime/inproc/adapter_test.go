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

package inproc

import (
	"context"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/agent/pkg/mem"
	"github.com/onexstack/onexai/internal/agent/pkg/runtime"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

type fakeEngine struct {
	messages []agentapi.Message
	options  agentapi.RunOptions
	events   chan agentapi.Event
	err      error
}

func (f *fakeEngine) Run(_ context.Context, messages []agentapi.Message, options agentapi.RunOptions) (<-chan agentapi.Event, error) {
	f.messages = messages
	f.options = options
	if f.err != nil {
		return nil, f.err
	}
	return f.events, nil
}

func (f *fakeEngine) Name() string { return "fake" }

type fakeResumable struct {
	options agentapi.ResumeOptions
	called  bool
	events  chan agentapi.Event
	err     error
}

func (f *fakeResumable) Resume(_ context.Context, options agentapi.ResumeOptions) (<-chan agentapi.Event, error) {
	f.called = true
	f.options = options
	if f.err != nil {
		return nil, f.err
	}
	return f.events, nil
}

type fakeStops struct {
	stopped string
}

func (f *fakeStops) Register(string, func()) func() { return func() {} }
func (f *fakeStops) RequestStop(_ context.Context, runID string) error {
	f.stopped = runID
	return nil
}
func (f *fakeStops) StopLocal(string) bool { return false }

func sendRequest(text string) *a2a.SendMessageRequest {
	return &a2a.SendMessageRequest{
		Message: &a2a.Message{
			ID:    "m1",
			Role:  a2a.MessageRoleUser,
			Parts: []*a2a.Part{a2a.NewTextPart(text)},
		},
	}
}

func TestRunPassesHistoryAndOptionsToEngine(t *testing.T) {
	events := make(chan agentapi.Event)
	engine := &fakeEngine{events: events}
	adapter := &Adapter{Engine: engine, Sessions: mem.NewSessionStore(nil, "", 0)}

	history := []agentapi.Message{{Role: agentapi.RoleUser, Content: "earlier"}}
	out, err := adapter.Run(context.Background(), &runtime.Request{
		A2A:        sendRequest("hello"),
		SessionKey: "s1",
		RunID:      "r1",
		History:    history,
		Options:    agentapi.RunOptions{Instruction: "be kind"},
	})
	require.NoError(t, err)
	assert.Equal(t, (<-chan agentapi.Event)(events), out)
	require.Len(t, engine.messages, 2)
	assert.Equal(t, "earlier", engine.messages[0].Content)
	assert.Equal(t, "hello", engine.messages[1].Content)
	assert.Equal(t, agentapi.RoleUser, engine.messages[1].Role)
	assert.Equal(t, "be kind", engine.options.Instruction)
}

func TestRunLoadsHistoryWhenNoneIsSupplied(t *testing.T) {
	sessions := mem.NewSessionStore(nil, "", 0)
	require.NoError(t, sessions.Append(context.Background(), "s1", agentapi.Message{Role: agentapi.RoleUser, Content: "old"}))

	engine := &fakeEngine{events: make(chan agentapi.Event)}
	adapter := &Adapter{Engine: engine, Sessions: sessions}
	_, err := adapter.Run(context.Background(), &runtime.Request{
		A2A:        sendRequest("new"),
		SessionKey: "s1",
		RunID:      "r1",
	})
	require.NoError(t, err)
	require.Len(t, engine.messages, 2)
	assert.Equal(t, "old", engine.messages[0].Content)
	assert.Equal(t, "new", engine.messages[1].Content)
}

func TestRunResumesWithHITLResponse(t *testing.T) {
	checkpoints := checkpoint.New(nil, "", 0)
	require.NoError(t, checkpoints.PutMeta(context.Background(), "r1", agentapi.RunMeta{
		RequestID: "pause-1",
		Pending:   []agentapi.PendingAddress{{Address: "tool-1"}},
	}))

	resumable := &fakeResumable{events: make(chan agentapi.Event)}
	adapter := &Adapter{Engine: &fakeEngine{}, Resumable: resumable, Checkpoints: checkpoints}

	req := &a2a.SendMessageRequest{Message: &a2a.Message{ID: "m2", Role: a2a.MessageRoleUser, Parts: []*a2a.Part{a2a.NewTextPart("allow")}}}
	require.NoError(t, runtime.SetHITLResponse(req, runtime.HITLResponse{
		RequestID: "pause-1",
		Decision:  agentapi.DecisionAllow,
		Answers:   map[string]string{"q1": "yes"},
	}))

	_, err := adapter.Run(context.Background(), &runtime.Request{A2A: req, RunID: "r1"})
	require.NoError(t, err)
	require.True(t, resumable.called)
	assert.Equal(t, "pause-1", resumable.options.RequestID)
	assert.Equal(t, agentapi.DecisionAllow, resumable.options.Decision)
	assert.Equal(t, map[string]string{"q1": "yes"}, resumable.options.Answers)
	assert.Len(t, resumable.options.Pending, 1)
}

func TestResumeRefusesAMismatchedRequestID(t *testing.T) {
	checkpoints := checkpoint.New(nil, "", 0)
	require.NoError(t, checkpoints.PutMeta(context.Background(), "r1", agentapi.RunMeta{RequestID: "pause-1"}))

	adapter := &Adapter{Engine: &fakeEngine{}, Resumable: &fakeResumable{}, Checkpoints: checkpoints}
	req := &a2a.SendMessageRequest{Message: &a2a.Message{ID: "m2", Role: a2a.MessageRoleUser, Parts: []*a2a.Part{a2a.NewTextPart("allow")}}}
	require.NoError(t, runtime.SetHITLResponse(req, runtime.HITLResponse{RequestID: "pause-2", Decision: agentapi.DecisionAllow}))

	_, err := adapter.Run(context.Background(), &runtime.Request{A2A: req, RunID: "r1"})
	require.ErrorIs(t, err, ErrNotPending)
}

func TestCancelRequestsStop(t *testing.T) {
	stops := &fakeStops{}
	adapter := &Adapter{Stops: stops}
	require.NoError(t, adapter.Cancel(context.Background(), "r1"))
	assert.Equal(t, "r1", stops.stopped)
}

func TestCardAdvertisesExtensions(t *testing.T) {
	card, err := (&Adapter{}).Card(context.Background())
	require.NoError(t, err)
	assert.True(t, card.Capabilities.Streaming)
	uris := make([]string, 0, len(card.Capabilities.Extensions))
	for _, ext := range card.Capabilities.Extensions {
		uris = append(uris, ext.URI)
	}
	assert.Contains(t, uris, "https://onexai.onexstack.io/a2a/extensions/hitl/v1")
	assert.Contains(t, uris, "https://onexai.onexstack.io/a2a/extensions/usage/v1")
}
