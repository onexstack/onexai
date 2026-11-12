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
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// scriptedAgentBiz replays a fixed event stream.
type scriptedAgentBiz struct {
	agentapi.IAgentBiz
	events []agentapi.Event
}

func (s *scriptedAgentBiz) Ask(context.Context, *agentapi.AskRequest) (<-chan agentapi.Event, error) {
	ch := make(chan agentapi.Event, len(s.events))
	for _, ev := range s.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

// stoppedRun is the shape a user-stopped run has: some answer deltas, no Done,
// then the terminal that says the run ended without finishing.
func stoppedRun() []agentapi.Event {
	return []agentapi.Event{
		{Type: agentapi.EventAnswer, Delta: "par"},
		{Type: agentapi.EventAnswer, Delta: "tial"},
		{Type: agentapi.EventStopped},
	}
}

func serveStoppedRun(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	agent := &scriptedAgentBiz{events: stoppedRun()}
	NewHandler(Deps{Biz: &fakeBiz{agent: agent}}).ChatCompletions(c)
	return w
}

// A stopped run is not a completed one. The partial text still reaches the
// caller, but the terminal frame says error and the stream does not end with
// [DONE], which a client reads as success.
func TestChatCompletionsReportsAStoppedRun(t *testing.T) {
	w := serveStoppedRun(t,
		`{"agent":"default","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	out := w.Body.String()
	for _, want := range []string{`"content":"par"`, `"content":"tial"`, `"finishReason":"error"`} {
		if !strings.Contains(out, want) {
			t.Errorf("stopped stream is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[DONE]") {
		t.Errorf("stopped stream ends with [DONE], which reads as success:\n%s", out)
	}
}

// The non-streaming branch reports the same two facts in one body: the partial
// answer it managed to produce, and a finish reason that says it did not finish.
func TestChatCompletionsSingleBodyKeepsAStoppedRunsPartialText(t *testing.T) {
	w := serveStoppedRun(t,
		`{"agent":"default","stream":false,"messages":[{"role":"user","content":"hi"}]}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"content":"partial"`) {
		t.Errorf("body does not carry the partial answer:\n%s", body)
	}
	if !strings.Contains(body, `"finishReason":"error"`) {
		t.Errorf("body does not mark the run as unfinished:\n%s", body)
	}
}
