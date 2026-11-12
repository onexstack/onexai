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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/agent/model"
)

type fakeRuns struct {
	run *model.RunM
}

func (f fakeRuns) GetRun(_ context.Context, _ string) (*model.RunM, error) {
	if f.run == nil {
		return nil, assert.AnError
	}
	return f.run, nil
}

func newRefreshHandler(t *testing.T, state string) (*TokenRefreshHandler, string) {
	t.Helper()
	issuer := NewIssuer([]byte("0123456789abcdef0123456789abcdef"))
	handler := &TokenRefreshHandler{
		Issuer: issuer,
		Runs:   fakeRuns{run: &model.RunM{ID: "run-1", State: state}},
	}
	token, err := issuer.Issue(Claims{
		RunID: "run-1", SessionKey: "s1", Agent: "a1", Subject: "u1",
		SubjectKind: "user", Scopes: []string{"skill", "task"}, Depth: 2, RootRunID: "root-1",
	}, time.Minute)
	require.NoError(t, err)
	return handler, token
}

func refresh(t *testing.T, handler *TokenRefreshHandler, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/runtime/token/refresh", nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestTokenRefreshRenewsALiveRun(t *testing.T) {
	handler, token := newRefreshHandler(t, model.RunStateRunning)

	recorder := refresh(t, handler, token)
	require.Equal(t, http.StatusOK, recorder.Code)

	var body struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.NotEmpty(t, body.Token)
	assert.Positive(t, body.ExpiresAt)

	claims, err := handler.Issuer.Verify(body.Token)
	require.NoError(t, err)
	// The renewed token is the same grant: nothing in the request body can add
	// a scope or move it to another run.
	assert.Equal(t, "run-1", claims.RunID)
	assert.Equal(t, "u1", claims.Subject)
	assert.Equal(t, []string{"skill", "task"}, claims.Scopes)
	assert.Equal(t, 2, claims.Depth)
	assert.Equal(t, "root-1", claims.RootRunID)
}

func TestTokenRefreshRefusesASettledRun(t *testing.T) {
	for _, state := range []string{model.RunStateCompleted, model.RunStateFailed, model.RunStateCanceled} {
		handler, token := newRefreshHandler(t, state)
		assert.Equal(t, http.StatusForbidden, refresh(t, handler, token).Code, "state %s", state)
	}
}

func TestTokenRefreshRefusesAMissingRun(t *testing.T) {
	issuer := NewIssuer([]byte("0123456789abcdef0123456789abcdef"))
	handler := &TokenRefreshHandler{Issuer: issuer, Runs: fakeRuns{}}
	token, err := issuer.Issue(Claims{RunID: "run-1", Agent: "a1"}, time.Minute)
	require.NoError(t, err)

	assert.Equal(t, http.StatusForbidden, refresh(t, handler, token).Code)
}

func TestTokenRefreshRefusesWhatItCannotVerify(t *testing.T) {
	handler, _ := newRefreshHandler(t, model.RunStateRunning)

	assert.Equal(t, http.StatusUnauthorized, refresh(t, handler, "").Code)
	assert.Equal(t, http.StatusUnauthorized, refresh(t, handler, "not-a-token").Code)
}

func TestTokenRefreshRefusesAnExpiredToken(t *testing.T) {
	issuer := NewIssuer([]byte("0123456789abcdef0123456789abcdef"))
	handler := &TokenRefreshHandler{
		Issuer: issuer,
		Runs:   fakeRuns{run: &model.RunM{ID: "run-1", State: model.RunStateRunning}},
	}
	token, err := issuer.Issue(Claims{RunID: "run-1", Agent: "a1"}, time.Minute)
	require.NoError(t, err)

	// Renewal keeps a live run going; it does not bring back a token that has
	// already lapsed.
	issuer.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	assert.Equal(t, http.StatusUnauthorized, refresh(t, handler, token).Code)
}
