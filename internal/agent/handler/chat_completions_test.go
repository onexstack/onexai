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
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	agentbiz "github.com/onexstack/onexai/internal/agent/biz/v1/agent"
	"github.com/onexstack/onexai/internal/agent/store"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// TestPausedAnswerNamesTheWayOut guards the one thing this endpoint can tell a
// caller whose run paused: which knob decides what happens instead. The message
// is the only place the config key is discoverable from the failing request, so
// losing it would leave the caller with an error and no next step.
func TestPausedAnswerNamesTheWayOut(t *testing.T) {
	if !strings.Contains(pausedAnswer, "compat-approval-mode") {
		t.Errorf("the paused answer %q does not name the setting that resolves it", pausedAnswer)
	}
}

// refusingAgentBiz refuses every run with the error a case is about.
type refusingAgentBiz struct {
	agentapi.IAgentBiz
	err error
}

func (r *refusingAgentBiz) Ask(context.Context, *agentapi.AskRequest) (<-chan agentapi.Event, error) {
	return nil, r.err
}

// callChatCompletions drives the handler with a JSON body and returns the
// recorder. The handler is built from the deps given, so a case can wire — or
// deliberately omit — the resource store the existence check reads.
func callChatCompletions(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.ChatCompletions(c)
	return w
}

// TestChatCompletionsRequiresAgent pins the contract change: the agent is
// required, and `model` is not a stand-in for it. Every case here has to be
// refused before the business layer is reached — a run that starts is a run
// that spends tokens on a request the caller got wrong.
func TestChatCompletionsRequiresAgent(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "no agent at all",
			body: `{"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "an agent that is only whitespace",
			body: `{"agent":"   ","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "a model is not an agent",
			body: `{"model":"fz-default","messages":[{"role":"user","content":"hi"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured := &capturingAgentBiz{}
			w := callChatCompletions(t, NewHandler(Deps{Biz: &fakeBiz{agent: captured}}), tc.body)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "agent is required") {
				t.Errorf("body = %s, want the missing-agent reason", w.Body.String())
			}
			if captured.got != nil {
				t.Fatal("the run reached the business layer")
			}
		})
	}
}

// TestChatCompletionsCarriesAgentAndModel pins that the two fields go to two
// different places: the agent to the selection, the model to the engine's model
// endpoint, and never the other way round.
func TestChatCompletionsCarriesAgentAndModel(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantAgents []string
		wantModel  string
	}{
		{
			name:       "the named agent is pinned",
			body:       `{"agent":"fz-public","messages":[{"role":"user","content":"hi"}]}`,
			wantAgents: []string{"fz-public"},
		},
		{
			name:       "surrounding space does not create a second name",
			body:       `{"agent":"  fz-public  ","messages":[{"role":"user","content":"hi"}]}`,
			wantAgents: []string{"fz-public"},
		},
		{
			name:       "the model travels as a model endpoint",
			body:       `{"agent":"fz-public","model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			wantAgents: []string{"fz-public"},
			wantModel:  "gpt-4o",
		},
		{
			name: "the sentinel pins no agent",
			body: `{"agent":"default","messages":[{"role":"user","content":"hi"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured := &capturingAgentBiz{}
			callChatCompletions(t, NewHandler(Deps{Biz: &fakeBiz{agent: captured}}), tc.body)

			if captured.got == nil {
				t.Fatal("the run never reached the business layer")
			}
			if got := captured.got.Selection.Agents; !slices.Equal(got, tc.wantAgents) {
				t.Errorf("Selection.Agents = %v, want %v", got, tc.wantAgents)
			}
			if captured.got.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", captured.got.Model, tc.wantModel)
			}
		})
	}
}

// TestChatCompletionsRefusesUnknownAgent covers the half a non-empty check
// cannot: loadAgentSet skips names it cannot resolve, so without this the run
// would answer normally with no instruction and no tools. The sentinel is the
// one value exempt, because it is not a row name.
func TestChatCompletionsRefusesUnknownAgent(t *testing.T) {
	deps := func(captured *capturingAgentBiz) Deps {
		return Deps{Biz: &fakeBiz{agent: captured}, Resources: store.NewEmptyStore()}
	}

	t.Run("an unknown name is refused", func(t *testing.T) {
		captured := &capturingAgentBiz{}
		w := callChatCompletions(t, NewHandler(deps(captured)),
			`{"agent":"no-such-agent","messages":[{"role":"user","content":"hi"}]}`)

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
		}
		if captured.got != nil {
			t.Fatal("the run reached the business layer")
		}
	})

	t.Run("the sentinel needs no row", func(t *testing.T) {
		captured := &capturingAgentBiz{}
		callChatCompletions(t, NewHandler(deps(captured)),
			`{"agent":"default","messages":[{"role":"user","content":"hi"}]}`)

		if captured.got == nil {
			t.Fatal("the sentinel was refused; it is the builtin assistant, not a row")
		}
	})
}

// TestChatCompletionsSkipsExistenceCheckWithoutResources keeps the handler
// usable where no resource store is wired: the check is an improvement, not a
// new hard dependency of the endpoint.
func TestChatCompletionsSkipsExistenceCheckWithoutResources(t *testing.T) {
	captured := &capturingAgentBiz{}
	callChatCompletions(t, NewHandler(Deps{Biz: &fakeBiz{agent: captured}}),
		`{"agent":"any-name","messages":[{"role":"user","content":"hi"}]}`)

	if captured.got == nil {
		t.Fatal("the request was refused although no store was wired to check it")
	}
}

// TestChatCompletionsMapsModelErrors pins the status a caller acts on after the
// model was wired through: the endpoint must answer the same way /v1/agent/ask
// does, rather than collapsing a refusal into an unknown 500.
func TestChatCompletionsMapsModelErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"another subject's endpoint", agentbiz.ErrModelForbidden, http.StatusForbidden},
		{"a disabled endpoint", agentbiz.ErrModelDisabled, http.StatusConflict},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler(Deps{Biz: &fakeBiz{agent: &refusingAgentBiz{err: tc.err}}})
			w := callChatCompletions(t, h,
				`{"agent":"fz-public","model":"someone-elses-endpoint","messages":[{"role":"user","content":"hi"}]}`)

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
