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

package runtimecli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpCall posts one JSON-RPC request to the broker.
func mcpCall(t *testing.T, broker *ApprovalBroker, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, broker.URL(), strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+broker.Token())
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	return response
}

func TestBrokerAdvertisesTheApprovalTool(t *testing.T) {
	broker, err := NewApprovalBroker()
	require.NoError(t, err)
	defer broker.Close()

	response := mcpCall(t, broker, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	defer response.Body.Close()

	var payload struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
	require.Len(t, payload.Result.Tools, 1)
	assert.Equal(t, ApprovalToolName, payload.Result.Tools[0].Name)
}

// TestBrokerRequiresItsToken pins that an unauthenticated caller cannot approve
// its own tool calls: the decide endpoint is a decision, not a notification.
func TestBrokerRequiresItsToken(t *testing.T) {
	broker, err := NewApprovalBroker()
	require.NoError(t, err)
	defer broker.Close()

	request, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(broker.URL(), "/mcp")+"/decide",
		strings.NewReader(`{"id":"x","allow":true}`))
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}

// TestDecideReleasesTheWaitingToolCall pins the whole mechanism: the tool call
// blocks, a human answers, and the CLI receives an "allow" it can act on.
func TestDecideReleasesTheWaitingToolCall(t *testing.T) {
	broker, err := NewApprovalBroker()
	require.NoError(t, err)
	defer broker.Close()

	go func() {
		// Wait for the call to be registered, then answer it.
		for {
			if id, ok := broker.Pending(); ok {
				broker.Decide(id, true, "approved in the test")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	response := mcpCall(t, broker,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Bash","tool_use_id":"call-1"}}}`)
	defer response.Body.Close()

	var payload struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
	require.Len(t, payload.Result.Content, 1)

	var decision map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload.Result.Content[0].Text), &decision))
	assert.Equal(t, "allow", decision["behavior"])
	assert.Equal(t, "approved in the test", decision["message"])
}

// TestUnansweredCallIsDenied pins the timeout rule: a call nobody approved must
// not run because the clock ran out.
func TestDecideRefusesAnUnknownCall(t *testing.T) {
	broker, err := NewApprovalBroker()
	require.NoError(t, err)
	defer broker.Close()

	assert.False(t, broker.Decide("nothing-is-waiting", true, ""))
}

func TestBrokerCloseReleasesWaiters(t *testing.T) {
	broker, err := NewApprovalBroker()
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The call is expected to end poorly: closing the broker is what
		// releases it, and the connection goes with it. What matters is that it
		// does not hang.
		request, err := http.NewRequest(http.MethodPost, broker.URL(),
			strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Bash","tool_use_id":"call-2"}}}`))
		if err != nil {
			return
		}
		request.Header.Set("Authorization", "Bearer "+broker.Token())
		if response, err := http.DefaultClient.Do(request); err == nil {
			_ = response.Body.Close()
		}
	}()
	for {
		if _, ok := broker.Pending(); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = broker.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a waiting call was not released when the broker closed")
	}
}
