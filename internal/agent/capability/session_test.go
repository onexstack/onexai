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

package capability

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
)

type fakeSessions struct {
	sessions map[string]*model.SessionM
	messages []*model.SessionMessageM
	created  []*model.SessionM
	copied   []*model.SessionMessageM
}

func (f *fakeSessions) GetSession(_ context.Context, id string) (*model.SessionM, error) {
	session, ok := f.sessions[id]
	if !ok {
		return nil, assert.AnError
	}
	return session, nil
}

func (f *fakeSessions) ListSession(_ context.Context, owner string, _ int) ([]*model.SessionM, error) {
	var out []*model.SessionM
	for _, session := range f.sessions {
		if session.Owner == owner {
			out = append(out, session)
		}
	}
	return out, nil
}

func (f *fakeSessions) ListMessage(_ context.Context, _ string) ([]*model.SessionMessageM, error) {
	return f.messages, nil
}

func (f *fakeSessions) CreateSession(_ context.Context, session *model.SessionM) error {
	f.created = append(f.created, session)
	return nil
}

func (f *fakeSessions) ReplaceMessages(_ context.Context, _ string, messages []*model.SessionMessageM) error {
	f.copied = messages
	return nil
}

func newSessionProvider(t *testing.T) (*SessionProvider, *fakeSessions, *fakeInvoker) {
	t.Helper()
	store := &fakeSessions{sessions: map[string]*model.SessionM{
		"mine": {
			ID: "mine", Agent: "planner", Owner: "u1", Name: "mine",
			State: model.SessionStateReady,
		},
		"theirs": {ID: "theirs", Agent: "planner", Owner: "u2"},
	}}
	store.messages = []*model.SessionMessageM{
		{Seq: 1, Role: "user", Content: "hello"},
		{Seq: 2, Role: "assistant", Content: "hi"},
	}
	invoker := &fakeInvoker{result: AgentResult{State: "completed", Text: "answer"}}
	delegation := &AgentProvider{Lookup: fakeLookup{}, Invoker: invoker}
	return &SessionProvider{Sessions: store, Invoker: invoker, Delegation: delegation}, store, invoker
}

func claimsFor(subject string) Claims {
	return Claims{RunID: "run-1", SessionKey: "s1", Agent: "planner", Subject: subject}
}

func TestSessionListIsScopedToTheCaller(t *testing.T) {
	provider, _, _ := newSessionProvider(t)
	result, err := provider.list(context.Background(), claimsFor("u1"), json.RawMessage(`{}`))
	require.NoError(t, err)
	sessions := result.(map[string]any)["sessions"].([]map[string]any)
	require.Len(t, sessions, 1)
	assert.Equal(t, "mine", sessions[0]["session"])
}

func TestSessionListNeedsACaller(t *testing.T) {
	provider, _, _ := newSessionProvider(t)
	_, err := provider.list(context.Background(), Claims{RunID: "r"}, json.RawMessage(`{}`))
	require.Error(t, err)
}

// Someone else's session and a missing one answer identically, so the tool
// cannot be used to probe for ids.
func TestSessionHistoryRefusesForeignSessions(t *testing.T) {
	provider, _, _ := newSessionProvider(t)
	_, err := provider.owned(context.Background(), claimsFor("u1"), "theirs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, err = provider.owned(context.Background(), claimsFor("u1"), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSessionHistoryReturnsMessages(t *testing.T) {
	provider, _, _ := newSessionProvider(t)
	result, err := provider.history(context.Background(), claimsFor("u1"), json.RawMessage(`{"session":"mine"}`))
	require.NoError(t, err)
	messages := result.(map[string]any)["messages"].([]map[string]any)
	require.Len(t, messages, 2)

	result, err = provider.history(context.Background(), claimsFor("u1"), json.RawMessage(`{"session":"mine","limit":1}`))
	require.NoError(t, err)
	messages = result.(map[string]any)["messages"].([]map[string]any)
	require.Len(t, messages, 1)
	assert.Equal(t, "hi", messages[0]["content"])
}

func TestSessionInvokeContinuesTheSessionsAgent(t *testing.T) {
	provider, _, invoker := newSessionProvider(t)
	_, err := provider.invoke(context.Background(), claimsFor("u1"),
		json.RawMessage(`{"session":"mine","message":"continue"}`))
	require.NoError(t, err)
	require.Equal(t, 1, invoker.calls)
	assert.Equal(t, "planner", invoker.call.Agent)
	assert.Equal(t, "mine", invoker.call.Session)
	assert.Equal(t, "continue", invoker.call.Message)
	assert.Equal(t, "u1", invoker.call.Subject)
}

func TestSessionInvokeRefusesForeignSessions(t *testing.T) {
	provider, _, invoker := newSessionProvider(t)
	_, err := provider.invoke(context.Background(), claimsFor("u1"),
		json.RawMessage(`{"session":"theirs","message":"continue"}`))
	require.Error(t, err)
	assert.Zero(t, invoker.calls)
}

func TestSessionForkCopiesUpToASequence(t *testing.T) {
	provider, store, _ := newSessionProvider(t)
	result, err := provider.fork(context.Background(), claimsFor("u1"),
		json.RawMessage(`{"session":"mine","upToSeq":1,"name":"branch"}`))
	require.NoError(t, err)
	require.Len(t, store.created, 1)
	assert.Equal(t, "branch", store.created[0].Name)
	assert.Equal(t, "planner", store.created[0].Agent)
	require.Len(t, store.copied, 1)
	assert.Equal(t, "hello", store.copied[0].Content)
	assert.Equal(t, 1, result.(map[string]any)["messages"])
}

func TestSessionProviderOffersNothingWithoutAStore(t *testing.T) {
	assert.Empty(t, (&SessionProvider{}).Tools())
}

func TestSessionProviderToolNames(t *testing.T) {
	provider, _, _ := newSessionProvider(t)
	var names []string
	for _, tool := range provider.Tools() {
		names = append(names, tool.QualifiedName())
	}
	assert.ElementsMatch(t, []string{"session_list", "session_history", "session_invoke", "session_fork"}, names)
}
