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

package authn

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authn/bearer"
	"github.com/onexstack/onexai/internal/pkg/authn/method"
)

func TestAgentCallFallsBackToThePlatformWithoutAuthWebhooks(t *testing.T) {
	platformCalled := false
	engine := gin.New()
	call := NewAgentCall(agentCallOptions(map[string]*model.AgentM{
		"plain": {Name: "plain", Namespace: "default"},
	}, nil))
	group := engine.Group("/v1", call.Wrap(platformAuthenticator(&platformCalled)))
	group.POST("/agent/ask", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"user": claimsOf(c)})
	})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/agent/ask",
		strings.NewReader(`{"selection":{"agents":["plain"]}}`)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if !platformCalled {
		t.Fatal("an agent without authentication webhooks must use the platform chain")
	}
}

func TestAgentCallAuthenticatesWithTheAgentsWebhook(t *testing.T) {
	platformCalled := false
	var body string
	webhook := tokenReviewServer(t, "good", "alice")

	engine := gin.New()
	call := NewAgentCall(agentCallOptions(map[string]*model.AgentM{
		"guarded": {Name: "guarded", Namespace: "default", AuthnWebhooks: model.StringList{"sso"}},
	}, map[string]*model.WebhookM{
		"sso": {Name: "sso", Status: model.StatusEnabled, Type: model.WebhookTypeAuthentication, URL: webhook.URL},
	}))
	group := engine.Group("/v1", call.Wrap(platformAuthenticator(&platformCalled)))
	group.POST("/agent/ask", func(c *gin.Context) {
		raw, _ := io.ReadAll(c.Request.Body)
		body = string(raw)
		m, _ := method.From(c.Request.Context())
		c.JSON(http.StatusOK, gin.H{"user": claimsOf(c), "method": m})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ask",
		strings.NewReader(`{"selection":{"agents":["guarded"]}}`))
	req.Header.Set("Authorization", "Bearer good")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if platformCalled {
		t.Fatal("the platform chain must not run when the agent's webhook authenticated the request")
	}
	var answer map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if answer["user"] != "alice" {
		t.Fatalf("handler saw user %q, want alice", answer["user"])
	}
	if answer["method"] != "webhook:sso" {
		t.Fatalf("method = %q, want webhook:sso", answer["method"])
	}
	if body != `{"selection":{"agents":["guarded"]}}` {
		t.Fatalf("handler body = %q; the middleware did not restore it", body)
	}
}

func TestAgentCallRejectsATokenTheWebhookRefuses(t *testing.T) {
	platformCalled := false
	webhook := tokenReviewServer(t, "good", "alice")

	engine := gin.New()
	call := NewAgentCall(agentCallOptions(map[string]*model.AgentM{
		"guarded": {Name: "guarded", AuthnWebhooks: model.StringList{"sso"}},
	}, map[string]*model.WebhookM{
		"sso": {Name: "sso", Status: model.StatusEnabled, Type: model.WebhookTypeAuthentication, URL: webhook.URL},
	}))
	group := engine.Group("/v1", call.Wrap(platformAuthenticator(&platformCalled)))
	group.POST("/agent/ask", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ask",
		strings.NewReader(`{"selection":{"agents":["guarded"]}}`))
	req.Header.Set("Authorization", "Bearer bad")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if platformCalled {
		t.Fatal("a refused token must not fall back to the platform chain")
	}
}

func TestAgentCallLeavesAKSKOnThePlatform(t *testing.T) {
	platformCalled := false
	webhook := tokenReviewServer(t, "good", "alice")

	engine := gin.New()
	call := NewAgentCall(agentCallOptions(map[string]*model.AgentM{
		"guarded": {Name: "guarded", AuthnWebhooks: model.StringList{"sso"}},
	}, map[string]*model.WebhookM{
		"sso": {Name: "sso", Status: model.StatusEnabled, Type: model.WebhookTypeAuthentication, URL: webhook.URL},
	}))
	group := engine.Group("/v1", call.Wrap(platformAuthenticator(&platformCalled)))
	group.POST("/agent/ask", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ask",
		strings.NewReader(`{"selection":{"agents":["guarded"]}}`))
	req.Header.Set("X-API-KEY", "ak")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if !platformCalled {
		t.Fatal("AK/SK is the platform's service credential and must stay on its chain")
	}
}

func TestAgentCallFailsClosedWhenTheNamedHookIsMissing(t *testing.T) {
	platformCalled := false
	engine := gin.New()
	call := NewAgentCall(agentCallOptions(map[string]*model.AgentM{
		"guarded": {Name: "guarded", AuthnWebhooks: model.StringList{"gone"}},
	}, nil))
	group := engine.Group("/v1", call.Wrap(platformAuthenticator(&platformCalled)))
	group.POST("/agent/ask", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ask",
		strings.NewReader(`{"selection":{"agents":["guarded"]}}`))
	req.Header.Set("Authorization", "Bearer good")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when the named hook does not exist", recorder.Code)
	}
}

func TestAgentCallResolvesTheInvokePathParameter(t *testing.T) {
	platformCalled := false
	webhook := tokenReviewServer(t, "good", "alice")

	engine := gin.New()
	call := NewAgentCall(agentCallOptions(map[string]*model.AgentM{
		"guarded": {Name: "guarded", AuthnWebhooks: model.StringList{"sso"}},
	}, map[string]*model.WebhookM{
		"sso": {Name: "sso", Status: model.StatusEnabled, Type: model.WebhookTypeAuthentication, URL: webhook.URL},
	}))
	group := engine.Group("/v1", call.Wrap(platformAuthenticator(&platformCalled)))
	group.POST("/agent/agents/:name/invoke", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/v1/agent/agents/guarded/invoke", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer good")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if platformCalled {
		t.Fatal("the invoke path names the agent in the URL; its webhook should have authenticated")
	}
}

// agentCallOptions wires the maps into the middleware's function dependencies.
func agentCallOptions(
	agents map[string]*model.AgentM, hooks map[string]*model.WebhookM,
) AgentCallOptions {
	return AgentCallOptions{
		LoadAgent: func(_ context.Context, ref string) (*model.AgentM, error) {
			return agents[ref], nil
		},
		ListWebhooks: func(context.Context) ([]*model.WebhookM, error) {
			out := make([]*model.WebhookM, 0, len(hooks))
			for _, hook := range hooks {
				out = append(out, hook)
			}
			return out, nil
		},
	}
}

// platformAuthenticator stands in for AKSKAuth/UnifiedAuth.
func platformAuthenticator(called *bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		*called = true
		bearer.SetClaims(c, &bearer.Claims{UserID: "platform-user"})
		c.Next()
	}
}

// tokenReviewServer answers authenticated only for the given token.
func tokenReviewServer(t *testing.T, goodToken, username string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var review struct {
			Spec struct {
				Token string `json:"token"`
			} `json:"spec"`
		}
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Errorf("decode token review: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": map[string]any{
				"authenticated": review.Spec.Token == goodToken,
				"user":          map[string]any{"username": username, "groups": []string{"oncall"}},
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func claimsOf(c *gin.Context) string {
	claims, ok := bearer.ClaimsFrom(c)
	if !ok {
		return ""
	}
	return claims.UserID
}
