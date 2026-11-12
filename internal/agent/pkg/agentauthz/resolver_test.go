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

package agentauthz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

func TestAuthorizerForUsesTheAgentsAuthorizationWebhook(t *testing.T) {
	server := subjectAccessReviewServer(t, true)
	resolver := New(Options{
		LoadAgent: func(context.Context, string) (*model.AgentM, error) {
			return &model.AgentM{
				Name:          "guarded",
				AuthzWebhooks: model.StringList{"policy"},
			}, nil
		},
		ListWebhooks: func(context.Context) ([]*model.WebhookM, error) {
			return []*model.WebhookM{{
				Name:   "policy",
				Status: model.StatusEnabled,
				Type:   model.WebhookTypeAuthorization,
				URL:    server.URL,
			}}, nil
		},
	})

	authorizer, found, err := resolver.AuthorizerFor(context.Background(), "guarded")
	if err != nil {
		t.Fatalf("AuthorizerFor: %v", err)
	}
	if !found {
		t.Fatal("the agent names an authorization webhook but the resolver reported none")
	}
	decision, reason, err := authorizer.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.SubjectFromUser("alice", "alice"),
		Verb:      authz.VerbRead,
		AgentName: "guarded",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("decision = %v (%s), want allow", decision, reason)
	}
}

func TestAuthorizerForReportsAgentsWithoutWebhooks(t *testing.T) {
	resolver := New(Options{
		LoadAgent: func(context.Context, string) (*model.AgentM, error) {
			return &model.AgentM{Name: "plain"}, nil
		},
		ListWebhooks: func(context.Context) ([]*model.WebhookM, error) { return nil, nil },
	})
	_, found, err := resolver.AuthorizerFor(context.Background(), "plain")
	if err != nil {
		t.Fatalf("AuthorizerFor: %v", err)
	}
	if found {
		t.Fatal("an agent with no authorization webhooks must use the platform path")
	}
}

func TestAuthorizerForFailsWhenTheNamedHookIsMissing(t *testing.T) {
	resolver := New(Options{
		LoadAgent: func(context.Context, string) (*model.AgentM, error) {
			return &model.AgentM{
				Name:          "guarded",
				AuthzWebhooks: model.StringList{"gone"},
			}, nil
		},
		ListWebhooks: func(context.Context) ([]*model.WebhookM, error) { return nil, nil },
	})
	if _, found, err := resolver.AuthorizerFor(context.Background(), "guarded"); err == nil {
		t.Fatalf("missing hook: found=%v err=nil; want a fail-closed error", found)
	}
}

func TestAuthorizerForFailsWhenTheHookIsWrongType(t *testing.T) {
	resolver := New(Options{
		LoadAgent: func(context.Context, string) (*model.AgentM, error) {
			return &model.AgentM{
				Name:          "guarded",
				AuthzWebhooks: model.StringList{"run-only"},
			}, nil
		},
		ListWebhooks: func(context.Context) ([]*model.WebhookM, error) {
			return []*model.WebhookM{{
				Name:   "run-only",
				Status: model.StatusEnabled,
				Type:   model.WebhookTypeRun,
			}}, nil
		},
	})
	if _, _, err := resolver.AuthorizerFor(context.Background(), "guarded"); err == nil {
		t.Fatal("a run hook named as an authorization hook must be an error")
	}
}

func TestAuthorizerForDeniesWhenTheWebhookDenies(t *testing.T) {
	server := subjectAccessReviewServer(t, false)
	resolver := New(Options{
		LoadAgent: func(context.Context, string) (*model.AgentM, error) {
			return &model.AgentM{
				Name:          "guarded",
				AuthzWebhooks: model.StringList{"policy"},
			}, nil
		},
		ListWebhooks: func(context.Context) ([]*model.WebhookM, error) {
			return []*model.WebhookM{{
				Name:   "policy",
				Status: model.StatusEnabled,
				Type:   model.WebhookTypeAuthorization,
				URL:    server.URL,
			}}, nil
		},
	})
	authorizer, _, err := resolver.AuthorizerFor(context.Background(), "guarded")
	if err != nil {
		t.Fatalf("AuthorizerFor: %v", err)
	}
	decision, _, err := authorizer.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.SubjectFromUser("mallory", "mallory"),
		Verb:      authz.VerbRead,
		AgentName: "guarded",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if decision != authz.DecisionDeny {
		t.Fatalf("decision = %v, want deny", decision)
	}
}

func subjectAccessReviewServer(t *testing.T, allowed bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Errorf("decode subject access review: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(authorizationv1.SubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{
				Allowed: allowed,
				Denied:  !allowed,
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}
