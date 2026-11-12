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

package casdoor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// enforceResponse is the {"status":"ok","data":[...]} envelope
// casdoorsdk.Client.Enforce expects from POST /api/enforce.
type enforceResponse struct {
	Status string `json:"status"`
	Msg    string `json:"msg,omitempty"`
	Data   any    `json:"data,omitempty"`
}

// newTestServer starts an httptest.Server that answers POST /api/enforce with
// the given envelope, and returns an Authorizer pointed at it. The request's
// decoded casbin triple (sub/obj/act) is handed to captureFn (if non-nil) so
// tests can assert on what this Authorizer actually sent.
func newTestServer(t *testing.T, resp enforceResponse, captureFn func(triple []any)) *Authorizer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captureFn != nil {
			var triple []any
			if err := json.NewDecoder(r.Body).Decode(&triple); err != nil {
				t.Fatalf("failed to decode request body: %v", err)
			}
			captureFn(triple)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	return NewAuthorizer(Config{
		Endpoint:     srv.URL,
		Organization: "built-in",
		Timeout:      2 * time.Second,
	})
}

func TestAuthorizerAllowsWhenCasdoorAllows(t *testing.T) {
	a := newTestServer(t, enforceResponse{Status: "ok", Data: []bool{true}}, nil)

	decision, reason, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:      authz.VerbRead,
		AgentName: "k8s-sre-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
	if reason == "" {
		t.Error("expected a non-empty reason")
	}
}

func TestAuthorizerTreatsCasdoorsFalseAsNoOpinionNotADeny(t *testing.T) {
	a := newTestServer(t, enforceResponse{Status: "ok", Data: []bool{false}}, nil)

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"},
		Verb:     authz.VerbRead,
		ToolName: "get-resource",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion: Enforce's false is ambiguous (no match vs explicit deny), so it must never be manufactured into a Deny", decision)
	}
}

func TestAuthorizerReportsAServerErrorAsNoOpinion(t *testing.T) {
	a := newTestServer(t, enforceResponse{Status: "error", Msg: "internal error"}, nil)

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:      authz.VerbRead,
		AgentName: "some-agent",
	})
	if err == nil {
		t.Fatal("expected an error when the Casdoor server reports status=error")
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion on execution error (not a business Deny)", decision)
	}
}

func TestAuthorizerReportsAnUnreachableCasdoorAsNoOpinion(t *testing.T) {
	a := NewAuthorizer(Config{Endpoint: "http://127.0.0.1:1", Organization: "built-in", Timeout: 200 * time.Millisecond})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:      authz.VerbRead,
		AgentName: "some-agent",
	})
	if err == nil {
		t.Fatal("expected an error for an unreachable endpoint")
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion on network failure", decision)
	}
}

func TestAuthorizerDoesNotCallCasdoorWithoutASubjectID(t *testing.T) {
	called := false
	a := newTestServer(t, enforceResponse{Status: "ok", Data: []bool{true}}, func([]any) { called = true })

	decision, reason, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: ""}, // no usable identifier
		Verb:      authz.VerbRead,
		AgentName: "some-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion for a Subject with no usable identifier", decision)
	}
	if reason == "" {
		t.Error("expected a non-empty reason explaining why no decision was made")
	}
	if called {
		t.Error("Casdoor should not be called when the Subject has no usable identifier")
	}
}

func TestAuthorizerPrefixesTheCasbinSubjectWithTheOrganization(t *testing.T) {
	var captured []any
	a := newTestServer(t, enforceResponse{Status: "ok", Data: []bool{true}}, func(triple []any) { captured = triple })

	if _, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:      authz.VerbRead,
		AgentName: "k8s-sre-agent",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(captured) != 3 {
		t.Fatalf("got %d-element triple, want 3 (sub, obj, act)", len(captured))
	}
	sub, _ := captured[0].(string)
	if sub != "built-in/u_alice" {
		t.Fatalf("got sub %q, want %q (Organization prefix + PrimaryID)", sub, "built-in/u_alice")
	}
}

func TestAuthorizerMapsTheObjectIDPerLevel(t *testing.T) {
	tests := []struct {
		name string
		req  authz.AuthorizationRequest
		want string
	}{
		{
			name: "agent level",
			req:  authz.AuthorizationRequest{AgentName: "k8s-sre-agent"},
			want: "agent:k8s-sre-agent",
		},
		{
			name: "tool level",
			req:  authz.AuthorizationRequest{ToolName: "get-resource"},
			want: "tool:get-resource",
		},
		{
			name: "resource level wins over tool name",
			req: authz.AuthorizationRequest{
				ToolName: "cmdb_search",
				Resource: authz.Resource{Kind: authz.ResourceKindService, Name: "payment-service"},
			},
			want: "service:payment-service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured []any
			a := newTestServer(t, enforceResponse{Status: "ok", Data: []bool{true}}, func(triple []any) { captured = triple })

			req := tt.req
			req.Subject = authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"}
			req.Verb = authz.VerbRead

			if _, _, err := a.Authorize(context.Background(), req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			obj, _ := captured[1].(string)
			act, _ := captured[2].(string)
			if obj != tt.want {
				t.Fatalf("got obj %q, want %q", obj, tt.want)
			}
			if act != "read" {
				t.Fatalf("got act %q, want read", act)
			}
		})
	}
}

func TestAuthorizerUsesABareSubjectIDWithoutAnOrganization(t *testing.T) {
	var captured []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var triple []any
		json.NewDecoder(r.Body).Decode(&triple)
		captured = triple
		json.NewEncoder(w).Encode(enforceResponse{Status: "ok", Data: []bool{true}})
	}))
	defer srv.Close()

	a := NewAuthorizer(Config{Endpoint: srv.URL, Timeout: time.Second}) // no Organization set

	if _, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:      authz.VerbRead,
		AgentName: "some-agent",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sub, _ := captured[0].(string)
	if sub != "u_alice" {
		t.Fatalf("got sub %q, want bare %q when Organization is unset", sub, "u_alice")
	}
}
