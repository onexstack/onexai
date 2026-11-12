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

	"github.com/onexstack/onexai/internal/pkg/authz"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// decisionAgentBiz answers CheckAgentAccess with whatever the test set.
type decisionAgentBiz struct {
	agentapi.IAgentBiz
	allowed bool
	reason  string
	err     error
	gotID   string
	gotName string
}

func (f *decisionAgentBiz) CheckAgentAccess(_ context.Context, subjectID, agentName string) (bool, string, error) {
	f.gotID, f.gotName = subjectID, agentName
	return f.allowed, f.reason, f.err
}

func newDecisionEngine(t *testing.T, agent *decisionAgentBiz) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewHandler(Deps{Biz: &fakeBiz{agent: agent}})
	engine := gin.New()
	h.ApplyTo(engine.Group("/v1"))
	return engine
}

// TestCheckAgentAccessMapsTheThreeAnswers pins the distinction the caller acts
// on: an explicit allow, a policy that said nothing (fail-closed, still a 200),
// and an evaluator failure (500). Collapsing the last two would tell a user
// "you lack permission" when the truth is "the policy is down".
func TestCheckAgentAccessMapsTheThreeAnswers(t *testing.T) {
	cases := []struct {
		name       string
		agent      *decisionAgentBiz
		wantStatus int
		wantBody   string
	}{
		{
			name:       "explicit allow",
			agent:      &decisionAgentBiz{allowed: true, reason: "opa: matched allow rule(s) allow-default"},
			wantStatus: http.StatusOK,
			wantBody:   `"allowed":true`,
		},
		{
			name:       "no opinion is a denial, not an error",
			agent:      &decisionAgentBiz{allowed: false, reason: "agent: authorization: all levels NoOpinion (fail-closed)"},
			wantStatus: http.StatusOK,
			wantBody:   `"allowed":false`,
		},
		{
			name:       "evaluator failure is a server error",
			agent:      &decisionAgentBiz{err: errors.New("opa: eval: boom")},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "could not be evaluated",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			engine := newDecisionEngine(t, tt.agent)
			w := doJSON(t, engine, http.MethodPost, "/v1/authz/decisions",
				`{"subjectID":"go.kong","agentName":"default"}`)
			if w.Code != tt.wantStatus {
				t.Fatalf("got status %d, want %d (body: %s)", w.Code, tt.wantStatus, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Fatalf("body %s does not contain %q", w.Body.String(), tt.wantBody)
			}
			if tt.agent.err == nil && (tt.agent.gotID != "go.kong" || tt.agent.gotName != "default") {
				t.Fatalf("handler passed (%q, %q), want (go.kong, default)", tt.agent.gotID, tt.agent.gotName)
			}
		})
	}
}

// TestCheckAgentAccessRequiresBothFields pins the 400: the question is
// meaningless without both halves, and answering it with an empty subject would
// evaluate a policy about nobody.
func TestCheckAgentAccessRequiresBothFields(t *testing.T) {
	engine := newDecisionEngine(t, &decisionAgentBiz{})

	for _, body := range []string{
		`{"agentName":"default"}`,
		`{"subjectID":"go.kong"}`,
		`{"subjectID":"  ","agentName":""}`,
		`not-json`,
	} {
		w := doJSON(t, engine, http.MethodPost, "/v1/authz/decisions", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: got status %d, want 400", body, w.Code)
		}
	}
}

// TestCheckAgentAccessHonoursTheCallerAllowlist pins the optional restriction:
// authz.decision-callers turns "any signed service may ask" into "the named
// services may ask", because every answer reveals part of the policy. An empty
// list keeps the endpoint open, which is what every deployment had.
func TestCheckAgentAccessHonoursTheCallerAllowlist(t *testing.T) {
	newEngine := func(callers map[string]struct{}) *gin.Engine {
		gin.SetMode(gin.TestMode)
		h := NewHandler(Deps{
			Biz:             &fakeBiz{agent: &decisionAgentBiz{allowed: true}},
			DecisionCallers: callers,
		})
		engine := gin.New()
		h.ApplyTo(engine.Group("/v1"))
		return engine
	}
	post := func(engine *gin.Engine, serviceName string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/authz/decisions",
			strings.NewReader(`{"subjectID":"go.kong","agentName":"default"}`))
		req.Header.Set("Content-Type", "application/json")
		if serviceName != "" {
			req = req.WithContext(authz.ContextWithSubject(req.Context(), authz.SubjectFromService(serviceName)))
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code
	}

	if got := post(newEngine(nil), "oai-chat-apiserver"); got != http.StatusOK {
		t.Fatalf("empty allowlist: got %d, want 200 (any signed caller)", got)
	}

	restricted := newEngine(map[string]struct{}{"oai-chat-apiserver": {}})
	if got := post(restricted, "oai-chat-apiserver"); got != http.StatusOK {
		t.Fatalf("listed caller: got %d, want 200", got)
	}
	if got := post(restricted, "some-other-service"); got != http.StatusForbidden {
		t.Fatalf("unlisted caller: got %d, want 403", got)
	}
	if got := post(restricted, ""); got != http.StatusForbidden {
		t.Fatalf("caller with no subject: got %d, want 403", got)
	}
}
