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
)

type fakeLookup map[string][]string

func (f fakeLookup) Delegates(_ context.Context, agent string) ([]string, error) {
	return f[agent], nil
}

type fakeInvoker struct {
	call   AgentCall
	result AgentResult
	err    error
	calls  int
}

func (f *fakeInvoker) Invoke(_ context.Context, call AgentCall) (AgentResult, error) {
	f.calls++
	f.call = call
	return f.result, f.err
}

func newTestAgentProvider(invoker AgentInvoker) *AgentProvider {
	return &AgentProvider{
		Lookup:  fakeLookup{"planner": {"researcher"}},
		Invoker: invoker,
	}
}

func TestAgentProviderAuthorizesDeclaredDelegate(t *testing.T) {
	invoker := &fakeInvoker{result: AgentResult{State: "completed", Text: "found it"}}
	provider := newTestAgentProvider(invoker)

	result, err := provider.Invoke(context.Background(), Claims{
		RunID: "run-1", SessionKey: "session-1", Agent: "planner", Subject: "u1",
		RootRunID: "root-1", Depth: 0,
	}, json.RawMessage(`{"agent":"researcher","message":"summarize the doc"}`))
	require.NoError(t, err)

	typed, ok := result.(AgentResult)
	require.True(t, ok)
	assert.Equal(t, "found it", typed.Text)

	require.Equal(t, 1, invoker.calls)
	assert.Equal(t, "researcher", invoker.call.Agent)
	assert.Equal(t, "session-1", invoker.call.ParentSession)
	assert.Equal(t, "run-1", invoker.call.ParentRunID)
	assert.Equal(t, "root-1", invoker.call.RootRunID)
	assert.Equal(t, 1, invoker.call.Depth)
	assert.Equal(t, "u1", invoker.call.Subject)
}

func TestAgentProviderRefusesUndeclaredDelegate(t *testing.T) {
	invoker := &fakeInvoker{}
	provider := newTestAgentProvider(invoker)

	_, err := provider.Invoke(context.Background(), Claims{Agent: "planner"},
		json.RawMessage(`{"agent":"billing","message":"refund this"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
	assert.Zero(t, invoker.calls)
}

func TestAgentProviderBoundsTheChain(t *testing.T) {
	invoker := &fakeInvoker{}
	provider := newTestAgentProvider(invoker)
	provider.MaxDepth = 2

	_, err := provider.Invoke(context.Background(), Claims{Agent: "planner", Depth: 2},
		json.RawMessage(`{"agent":"researcher","message":"go deeper"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds the limit")
	assert.Zero(t, invoker.calls)

	_, err = provider.Invoke(context.Background(), Claims{Agent: "planner", Depth: 1},
		json.RawMessage(`{"agent":"researcher","message":"go deeper"}`))
	require.NoError(t, err)
	assert.Equal(t, 2, invoker.call.Depth)
}

func TestAgentProviderValidatesArguments(t *testing.T) {
	provider := newTestAgentProvider(&fakeInvoker{})
	for _, args := range []string{
		`{"message":"hi"}`,
		`{"agent":"researcher"}`,
		`{"agent":"researcher","taskId":"t1"}`,
	} {
		_, err := provider.Invoke(context.Background(), Claims{Agent: "planner"}, json.RawMessage(args))
		require.Error(t, err, args)
	}
}

func TestAgentProviderToolIsScopedAndVisible(t *testing.T) {
	provider := newTestAgentProvider(&fakeInvoker{})
	tools := provider.Tools()
	require.Len(t, tools, 1)
	assert.Equal(t, "agent_invoke_agent", tools[0].QualifiedName())
	assert.Equal(t, "agent", tools[0].Namespace)
}

func TestAgentProviderWithoutInvokerOffersNoTools(t *testing.T) {
	provider := &AgentProvider{Lookup: fakeLookup{}}
	assert.Empty(t, provider.Tools())
}
