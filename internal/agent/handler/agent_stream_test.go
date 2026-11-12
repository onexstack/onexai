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

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/onexstack/onexai/internal/agent/biz"
	"github.com/onexstack/onexai/internal/pkg/authz"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// fakeAgentBiz is the business layer reduced to the one answer a test controls.
type fakeAgentBiz struct {
	agentapi.IAgentBiz
	askErr     error
	approveErr error
}

func (f *fakeAgentBiz) Ask(context.Context, *agentapi.AskRequest) (<-chan agentapi.Event, error) {
	return nil, f.askErr
}

func (f *fakeAgentBiz) Approve(context.Context, *agentapi.ApproveRequest) (<-chan agentapi.Event, error) {
	return nil, f.approveErr
}

// fakeBiz supplies the agent business logic and nothing else: the embedded nil
// interface panics loudly if a handler reaches for more than this test wired.
type fakeBiz struct {
	biz.IBiz
	agent agentapi.IAgentBiz
}

func (f *fakeBiz) Agent() agentapi.IAgentBiz { return f.agent }

// TestAuthorizationRefusalsAnswerForbiddenOnBothAgentPaths pins the status a
// caller acts on. Resuming a paused run re-runs the same gate the original
// request passed, so both paths have to answer the same way — and 500, which
// the approve path used to answer with, tells the client to report a bug
// instead of the one thing it can do.
func TestAuthorizationRefusalsAnswerForbiddenOnBothAgentPaths(t *testing.T) {
	denied := &authz.ErrAgentAccessDenied{
		Subject:   authz.SubjectFromUser("ou_684c62db025820b3af793e43a8b549ea", "someone"),
		AgentName: "default",
		Reason:    "agent: authorization: all levels NoOpinion (fail-closed)",
	}

	cases := []struct {
		name    string
		body    string
		handler func(*Handler, *gin.Context)
		agent   agentapi.IAgentBiz
	}{
		{"ask", `{}`, (*Handler).AskAgent, &fakeAgentBiz{askErr: denied}},
		{"approve", `{"runID":"run-1"}`, (*Handler).ApproveAgent, &fakeAgentBiz{approveErr: denied}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, status := serveAgentCall(t, tc.agent, tc.handler, tc.body)
			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", status, body)
			}
			if !strings.Contains(body, "PermissionDenied") {
				t.Errorf("body = %s, want the PermissionDenied reason", body)
			}
		})
	}
}

// TestAnUnrelatedFailureStaysAServerFault guards the mapping from widening: a
// failure that is not a refusal must still read as one, not be dressed up as
// something the caller can fix by asking differently.
func TestAnUnrelatedFailureStaysAServerFault(t *testing.T) {
	body, status := serveAgentCall(t,
		&fakeAgentBiz{approveErr: errors.New("the store is unreachable")},
		(*Handler).ApproveAgent, `{"runID":"run-1"}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", status, body)
	}
}

// serveAgentCall drives one handler over a fake biz and returns what it wrote.
func serveAgentCall(
	t *testing.T, agent agentapi.IAgentBiz, call func(*Handler, *gin.Context), body string,
) (string, int) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/agent", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	call(NewHandler(Deps{Biz: &fakeBiz{agent: agent}}), c)
	return w.Body.String(), w.Code
}
