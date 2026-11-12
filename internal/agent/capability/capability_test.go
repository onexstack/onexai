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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/mcpserver/pkg/protocol"
)

func TestTokenRoundTripAndExpiry(t *testing.T) {
	issuer := NewIssuer([]byte("0123456789abcdef0123456789abcdef"))
	token, err := issuer.Issue(Claims{
		RunID: "r1", SessionKey: "s1", Agent: "a1", Subject: "u1",
		Scopes: []string{"skill", "kb"},
	}, time.Minute)
	require.NoError(t, err)

	claims, err := issuer.Verify(token)
	require.NoError(t, err)
	assert.Equal(t, "r1", claims.RunID)
	assert.True(t, claims.HasScope("skill"))
	assert.False(t, claims.HasScope("sandbox"))

	_, err = issuer.Verify(token + "x")
	require.ErrorIs(t, err, ErrInvalidToken)

	expired := NewIssuer([]byte("0123456789abcdef0123456789abcdef"))
	expired.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	_, err = expired.Verify(token)
	require.ErrorIs(t, err, ErrInvalidToken)
}

type fakeProvider struct{}

func (fakeProvider) Namespace() string { return "skill" }
func (fakeProvider) Tools() []Tool {
	return []Tool{{
		Namespace: "skill", Name: "ping", Description: "ping",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, claims Claims, _ json.RawMessage) (any, error) {
			return map[string]any{"run": claims.RunID}, nil
		},
	}}
}

func newTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	registry := NewRegistry()
	registry.Register(fakeProvider{})
	issuer := NewIssuer([]byte("0123456789abcdef0123456789abcdef"))
	service := NewService(registry, issuer, nil)
	token, err := issuer.Issue(Claims{RunID: "r1", Agent: "a1", Scopes: []string{"skill"}}, time.Minute)
	require.NoError(t, err)
	return &Handler{Service: service}, token
}

func TestMCPInitializeAndList(t *testing.T) {
	handler, token := newTestHandler(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var response protocol.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	result, ok := response.Result.(map[string]any)
	require.True(t, ok)
	tools, ok := result["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	assert.Equal(t, "skill_ping", tools[0].(map[string]any)["name"])
}

func TestMCPCallAndScopeRefusal(t *testing.T) {
	handler, token := newTestHandler(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"skill_ping","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `\"run\":\"r1\"`)

	body = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sandbox_exec","arguments":{}}}`
	req = httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Contains(t, rec.Body.String(), "unknown tool")
}

func TestMCPUnauthorized(t *testing.T) {
	handler, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
