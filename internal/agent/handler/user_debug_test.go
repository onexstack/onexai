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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	agent "github.com/onexstack/onexai/pkg/agentapi"
	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

// callUserAsk drives UserAskAgent with the claims the self-service group's
// authentication would have stored. It returns the recorder so a case can
// assert a refusal, and the handler's business layer is a fake the caller
// supplies.
func callUserAsk(t *testing.T, h *Handler, claims *bearer.Claims, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/user/agent/ask", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("jwt_claims", claims)
	h.UserAskAgent(c)
	return w
}

func userAskHandler(captured *capturingAgentBiz) *Handler {
	return NewHandler(Deps{Biz: &fakeBiz{agent: captured}})
}

// TestUserAskBuiltinRunsWithoutSelection pins the one thing that makes the
// smart-create flow safe: "default" selects the builtin assistant (no tools,
// skills, MCPs or knowledge bases), and the identity is the token's, never
// anything the request could ask for.
func TestUserAskBuiltinRunsWithoutSelection(t *testing.T) {
	captured := &capturingAgentBiz{}
	w := callUserAsk(t, userAskHandler(captured),
		&bearer.Claims{UserID: "u1", Name: "Ada"}, `{"selection":{"agents":["default"]},"input":{"text":"hi"}}`)

	require.NotNil(t, captured.got, "the run did not reach the business layer: %s", w.Body.String())
	assert.Empty(t, captured.got.Selection.Agents)
	assert.Empty(t, captured.got.Selection.InvokedSkills)
	assert.Nil(t, captured.got.Capabilities)
	assert.Equal(t, "u1", captured.got.Subject.ID)
	assert.Equal(t, "Ada", captured.got.Subject.Name)
	require.NotNil(t, captured.got.Actor)
	assert.Equal(t, "u1", captured.got.Actor.ID)
	assert.Equal(t, agent.MeteringUnspecified, captured.got.Metering)
	assert.Equal(t, SourceConsole, captured.got.Source)
	assert.Equal(t, agent.ApprovalChannelNone, captured.got.Options.Approval.Channel)
	assert.True(t, strings.HasPrefix(captured.got.SessionID, "usr:default:u1:"))
	assert.Contains(t, captured.got.SessionID, captured.got.RunID)
}

// TestUserAskBuiltinSessionIsPerRequest guards the stateless contract: two
// matching calls must not read each other's history or share a concurrency
// lease.
func TestUserAskBuiltinSessionIsPerRequest(t *testing.T) {
	first, second := &capturingAgentBiz{}, &capturingAgentBiz{}
	claims := &bearer.Claims{UserID: "u1"}

	callUserAsk(t, userAskHandler(first), claims, `{"selection":{"agents":["default"]},"input":{"text":"hi"}}`)
	callUserAsk(t, userAskHandler(second), claims, `{"selection":{"agents":["default"]},"input":{"text":"hi"}}`)

	require.NotNil(t, first.got)
	require.NotNil(t, second.got)
	assert.NotEqual(t, first.got.SessionID, second.got.SessionID)
	assert.NotEqual(t, first.got.RunID, second.got.RunID)
}

// TestUserAskBuiltinRejectsCapabilities keeps the builtin branch honest: a
// caller that named a capability must not be told it was applied to a run that
// has no tools at all.
func TestUserAskBuiltinRejectsCapabilities(t *testing.T) {
	captured := &capturingAgentBiz{}
	w := callUserAsk(t, userAskHandler(captured), &bearer.Claims{UserID: "u1"},
		`{"selection":{"agents":["default"]},"input":{"text":"hi"},"capabilities":{"skills":["x"]}}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Nil(t, captured.got, "a rejected request reached the business layer")
}

// TestUserAskBuiltinCapsQuestion bounds the cost of the new browser path even
// if a client ignores the console's own limit.
func TestUserAskBuiltinCapsQuestion(t *testing.T) {
	captured := &capturingAgentBiz{}
	question := strings.Repeat("a", maxBuiltinQuestionBytes+1)
	w := callUserAsk(t, userAskHandler(captured), &bearer.Claims{UserID: "u1"},
		fmt.Sprintf(`{"selection":{"agents":["default"]},"input":{"text":%q}}`, question))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Nil(t, captured.got)
}

// TestUserAskNamedAgentUnchanged pins the existing path: a real agent still
// pins its selection, keeps its stable per-agent session, and forwards a
// capability subset.
func TestUserAskNamedAgentUnchanged(t *testing.T) {
	captured := &capturingAgentBiz{}
	w := callUserAsk(t, userAskHandler(captured), &bearer.Claims{UserID: "u1"},
		`{"selection":{"agents":["helper"]},"input":{"text":"hi"},"capabilities":{"skills":["s"]}}`)

	require.NotNil(t, captured.got, "the run did not reach the business layer: %s", w.Body.String())
	assert.Equal(t, []string{"helper"}, captured.got.Selection.Agents)
	assert.Equal(t, "usr:helper:u1", captured.got.SessionID)
	require.NotNil(t, captured.got.Capabilities)
	assert.Equal(t, []string{"s"}, captured.got.Capabilities.Skills)
}

// TestUserAskHostlessCompositionRunsAsCaller pins the playground contract: an
// empty agent with a composition runs as the caller, has no agent selection,
// forwards the composition model, and stays interruptible.
func TestUserAskHostlessCompositionRunsAsCaller(t *testing.T) {
	captured := &capturingAgentBiz{}
	w := callUserAsk(t, userAskHandler(captured), &bearer.Claims{UserID: "u1", Name: "Ada"},
		`{"input":{"text":"hi"},"composition":{"prompts":["persona"],"model":"fast"},"sessionNonce":"n1"}`)

	require.NotNil(t, captured.got, "the run did not reach the business layer: %s", w.Body.String())
	assert.Empty(t, captured.got.Selection.Agents, "a hostless run must not name an agent")
	assert.Nil(t, captured.got.Capabilities)
	require.NotNil(t, captured.got.Composition)
	assert.Equal(t, []string{"persona"}, captured.got.Composition.Prompts)
	assert.Equal(t, "fast", captured.got.Model)
	assert.Equal(t, agent.MeteringUnspecified, captured.got.Metering)
	assert.Equal(t, "u1", captured.got.Subject.ID)
	assert.Equal(t, "Ada", captured.got.Subject.Name)
	require.NotNil(t, captured.got.Actor)
	assert.Equal(t, "u1", captured.got.Actor.ID)
	assert.True(t, strings.HasPrefix(captured.got.SessionID, "usr:compose:"))
	assert.NotEqual(t, agent.ApprovalChannelNone, captured.got.Options.Approval.Channel,
		"a playground run must be able to ask for approval")
}

// TestUserAskHostlessSessionIsStablePerCompositionAndNonce keeps two debug
// conversations apart while letting the same one continue.
func TestUserAskHostlessSessionIsStablePerCompositionAndNonce(t *testing.T) {
	body := `{"input":{"text":"hi"},"composition":{"prompts":["persona"]},"sessionNonce":"n1"}`
	first, second, otherNonce, named := &capturingAgentBiz{}, &capturingAgentBiz{}, &capturingAgentBiz{}, &capturingAgentBiz{}
	claims := &bearer.Claims{UserID: "u1"}

	callUserAsk(t, userAskHandler(first), claims, body)
	callUserAsk(t, userAskHandler(second), claims, body)
	callUserAsk(t, userAskHandler(otherNonce), claims,
		`{"input":{"text":"hi"},"composition":{"prompts":["persona"]},"sessionNonce":"n2"}`)
	callUserAsk(t, userAskHandler(named), claims,
		`{"selection":{"agents":["helper"]},"input":{"text":"hi"},"composition":{"prompts":["persona"]},"sessionNonce":"n1"}`)

	require.NotNil(t, first.got)
	require.NotNil(t, second.got)
	require.NotNil(t, otherNonce.got)
	require.NotNil(t, named.got)
	assert.Equal(t, first.got.SessionID, second.got.SessionID)
	assert.NotEqual(t, first.got.SessionID, otherNonce.got.SessionID)
	assert.NotEqual(t, first.got.SessionID, named.got.SessionID)
	assert.NotEqual(t, first.got.SessionID, "usr:default:u1:"+first.got.RunID)
}

// TestUserAskRejectsHostlessWithoutComposition keeps the protocol honest: an
// empty agent only means something when the composition defines the run.
func TestUserAskRejectsHostlessWithoutComposition(t *testing.T) {
	captured := &capturingAgentBiz{}
	w := callUserAsk(t, userAskHandler(captured), &bearer.Claims{UserID: "u1"},
		`{"input":{"text":"hi"}}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Nil(t, captured.got, "a rejected request reached the business layer")
}

// TestUserAskRejectsAnonymous keeps anonymous claims out of quota, session and
// create ownership even if a future mount turns anonymous auth on.
func TestUserAskRejectsAnonymous(t *testing.T) {
	for _, claims := range []*bearer.Claims{{UserID: "anonymous"}, {UserID: ""}} {
		captured := &capturingAgentBiz{}
		w := callUserAsk(t, userAskHandler(captured), claims, `{"selection":{"agents":["default"]},"input":{"text":"hi"}}`)

		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Nil(t, captured.got)
	}
}

// TestUserAskForwardsImages pins the console image path on the self-service
// surface: the shared request's ImageRef list reaches the run unchanged.
func TestUserAskForwardsImages(t *testing.T) {
	captured := &capturingAgentBiz{}
	w := callUserAsk(t, userAskHandler(captured), &bearer.Claims{UserID: "u1"},
		`{"selection":{"agents":["helper"]},"input":{"text":"what is this?","images":[{"asset_id":"img_abc"}]}}`)

	require.NotNil(t, captured.got, "the run did not reach the business layer: %s", w.Body.String())
	require.Len(t, captured.got.Input.Images, 1)
	assert.Equal(t, "img_abc", captured.got.Input.Images[0].AssetID)
}

// TestUserAskBuiltinRejectsImages keeps the stateless text generation honest.
func TestUserAskBuiltinRejectsImages(t *testing.T) {
	captured := &capturingAgentBiz{}
	w := callUserAsk(t, userAskHandler(captured), &bearer.Claims{UserID: "u1"},
		`{"selection":{"agents":["default"]},"input":{"text":"hi","images":[{"asset_id":"img_abc"}]}}`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Nil(t, captured.got, "a rejected request reached the business layer")
}

// TestUserAskRejectsServerOwnedFields is the boundary the shared message must
// keep: a browser that sends an identity or lifecycle field is refused.
func TestUserAskRejectsServerOwnedFields(t *testing.T) {
	for _, body := range []string{
		`{"selection":{"agents":["helper"]},"input":{"text":"hi"},"subject":{"id":"someone-else"}}`,
		`{"selection":{"agents":["helper"]},"input":{"text":"hi"},"sessionId":"shared"}`,
		`{"selection":{"agents":["helper"]},"input":{"text":"hi"},"metering":{"mode":"MODE_CALLER"}}`,
		`{"selection":{"agents":["helper"]},"input":{"text":"hi"},"model":"gpt-4o"}`,
		`{"selection":{"agents":["helper"]},"input":{"text":"hi"},"options":{}}`,
		`{"selection":{"agents":["helper"]},"input":{"text":"hi"},"metadata":{"tenant":"x"}}`,
	} {
		captured := &capturingAgentBiz{}
		w := callUserAsk(t, userAskHandler(captured), &bearer.Claims{UserID: "u1"}, body)
		assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", body)
		assert.Nil(t, captured.got, "a rejected request reached the business layer: %s", body)
	}
}

// TestHasAnyCapability treats a present-but-empty selection as "narrowing
// nothing", so the builtin branch does not reject a client for shape alone.
func TestHasAnyCapability(t *testing.T) {
	assert.False(t, hasAnyCapability(nil))
	assert.False(t, hasAnyCapability(&v1.CapabilitySelection{}))
	assert.True(t, hasAnyCapability(&v1.CapabilitySelection{Skills: []string{"s"}}))
	assert.True(t, hasAnyCapability(&v1.CapabilitySelection{Mcps: []string{"m"}}))
	assert.True(t, hasAnyCapability(&v1.CapabilitySelection{KnowledgeBases: []string{"k"}}))
	assert.True(t, hasAnyCapability(&v1.CapabilitySelection{CustomCommands: []string{"c"}}))
}
