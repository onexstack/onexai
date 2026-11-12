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

package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestAuthorizerAllowsWhenTheEndpointAllows(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Spec.User != "u_alice" {
			t.Errorf("got user %q, want u_alice", req.Spec.User)
		}
		if req.Spec.ResourceAttributes == nil || req.Spec.ResourceAttributes.Resource != "agent" {
			t.Errorf("got resource attributes %+v, want agent", req.Spec.ResourceAttributes)
		}
		_ = json.NewEncoder(w).Encode(authorizationv1.SubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true, Reason: "approved"},
		})
	})

	a := NewAuthorizer("test", srv.URL, time.Second, FailureModeNoOpinion)
	decision, reason, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:    authz.VerbRead, AgentName: "some-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
	if reason != "approved" {
		t.Fatalf("got reason %q, want %q", reason, "approved")
	}
}

func TestAuthorizerDeniesWhenTheEndpointDenies(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(authorizationv1.SubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{Denied: true, Reason: "not on the allowlist"},
		})
	})

	a := NewAuthorizer("test", srv.URL, time.Second, FailureModeNoOpinion)
	decision, reason, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"},
		Verb:    authz.VerbRead, ToolName: "dangerous-tool",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionDeny {
		t.Fatalf("got %v, want Deny", decision)
	}
	if reason != "not on the allowlist" {
		t.Fatalf("got reason %q, want the endpoint's reason", reason)
	}
}

func TestAuthorizerPassesThroughANoOpinionResponse(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(authorizationv1.SubjectAccessReview{})
	})

	a := NewAuthorizer("test", srv.URL, time.Second, FailureModeNoOpinion)
	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion", decision)
	}
}

func TestAuthorizerTreatsEvaluationErrorByFailureMode(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(authorizationv1.SubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{EvaluationError: "policy compile failed"},
		})
	})

	req := authz.AuthorizationRequest{Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"}}

	decision, _, err := NewAuthorizer("test", srv.URL, time.Second, FailureModeNoOpinion).Authorize(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error for an evaluation error")
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion", decision)
	}

	decision, _, err = NewAuthorizer("test", srv.URL, time.Second, FailureModeDeny).Authorize(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error for an evaluation error")
	}
	if decision != authz.DecisionDeny {
		t.Fatalf("got %v, want Deny under FailureModeDeny", decision)
	}
}

func TestAuthorizerReportsAnUnreachableEndpointByFailureMode(t *testing.T) {
	req := authz.AuthorizationRequest{Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"}}

	decision, _, err := NewAuthorizer("test", "http://127.0.0.1:1", 200*time.Millisecond, FailureModeNoOpinion).Authorize(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error for an unreachable endpoint")
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion on network failure under the default FailureMode", decision)
	}

	decision, _, err = NewAuthorizer("test", "http://127.0.0.1:1", 200*time.Millisecond, FailureModeDeny).Authorize(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error for an unreachable endpoint")
	}
	if decision != authz.DecisionDeny {
		t.Fatalf("got %v, want Deny on network failure under FailureModeDeny", decision)
	}
}

func TestAuthorizerTreatsANon2xxStatusAsFailure(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	a := NewAuthorizer("test", srv.URL, time.Second, FailureModeNoOpinion)
	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"},
	})
	if err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion", decision)
	}
}

func TestAuthorizerAcceptsCreatedStatus(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(authorizationv1.SubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true},
		})
	})

	a := NewAuthorizer("test", srv.URL, time.Second, FailureModeNoOpinion)
	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
}

func TestToSubjectAccessReviewCarriesTheSubjectAndGroups(t *testing.T) {
	req := authz.AuthorizationRequest{
		Subject: authz.Subject{
			Kind:   authz.SubjectKindUser,
			UserID: "u1",
			Extra:  map[string]string{"group_codes": "g1,g2"},
		},
		Verb:       authz.VerbRead,
		AgentNames: []string{"a", "b"},
		ToolName:   "restart",
		MCPName:    "k8sgpt",
	}
	out := toSubjectAccessReview(req)

	if out.APIVersion != authorizationv1.SchemeGroupVersion.String() || out.Kind != "SubjectAccessReview" {
		t.Fatalf("got %s/%s, want authorization.k8s.io/v1 SubjectAccessReview", out.APIVersion, out.Kind)
	}
	if out.Spec.User != "u1" {
		t.Fatalf("got user %q, want u1", out.Spec.User)
	}
	if len(out.Spec.Groups) != 2 || out.Spec.Groups[0] != "g1" || out.Spec.Groups[1] != "g2" {
		t.Fatalf("got groups %v, want [g1 g2]", out.Spec.Groups)
	}
	if got := out.Spec.Extra["onexai.io/agent-names"]; len(got) != 1 || got[0] != "a,b" {
		t.Fatalf("got agent names %v, want [a,b]", got)
	}
	if got := out.Spec.Extra["onexai.io/tool-name"]; len(got) != 1 || got[0] != "restart" {
		t.Fatalf("got tool %v, want restart", got)
	}
}

func TestToSubjectAccessReviewIncludesAConcreteResource(t *testing.T) {
	req := authz.AuthorizationRequest{
		Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u1"},
		Verb:    authz.VerbWrite,
		Resource: authz.Resource{
			Kind:       authz.ResourceKindService,
			Name:       "payment-service",
			Attributes: map[string]string{"env": "prod", "namespace": "payments"},
		},
	}
	out := toSubjectAccessReview(req)

	if out.Spec.ResourceAttributes == nil {
		t.Fatal("expected resource attributes")
	}
	if out.Spec.ResourceAttributes.Verb != "update" {
		t.Fatalf("got verb %q, want update", out.Spec.ResourceAttributes.Verb)
	}
	if out.Spec.ResourceAttributes.Resource != "service" || out.Spec.ResourceAttributes.Name != "payment-service" {
		t.Fatalf("got %+v, want service/payment-service", out.Spec.ResourceAttributes)
	}
	if out.Spec.ResourceAttributes.Namespace != "payments" {
		t.Fatalf("got namespace %q, want payments", out.Spec.ResourceAttributes.Namespace)
	}
	if got := out.Spec.Extra["onexai.io/attr/env"]; len(got) != 1 || got[0] != "prod" {
		t.Fatalf("got attribute %v, want prod", got)
	}
}

// TestToSubjectAccessReviewIdentifiesAServiceAccount pins the identity a
// serviceaccount caller carries to the webhook. It used to arrive with only
// Subject.Kind set, so every internal caller looked alike — a receiver could
// neither distinguish them nor write a rule about one.
func TestToSubjectAccessReviewIdentifiesAServiceAccount(t *testing.T) {
	req := authz.AuthorizationRequest{
		Subject: authz.Subject{Kind: authz.SubjectKindServiceAccount, ServiceName: "oai-chatserver"},
		Verb:    authz.VerbRead, ToolName: "restart", MCPName: "k8sgpt",
		AgentNames: []string{"a", "b"},
	}
	out := toSubjectAccessReview(req)

	if out.Spec.User != "oai-chatserver" {
		t.Fatalf("got user %q, want oai-chatserver", out.Spec.User)
	}
	if got := out.Spec.Extra["onexai.io/subject-kind"]; len(got) != 1 || got[0] != "serviceaccount" {
		t.Fatalf("got subject kind %v, want serviceaccount", got)
	}
}

// TestSignatureCoversTheExactBody pins the receiver's side of the contract: the
// header is HMAC-SHA256 of the bytes that were sent, so a receiver can verify
// without parsing, and a body altered in transit does not verify.
func TestSignatureCoversTheExactBody(t *testing.T) {
	body := []byte(`{"apiVersion":"authorization.k8s.io/v1"}`)
	sig := Signature("s3cret", body)
	if sig == "" || !strings.HasPrefix(sig, "sha256=") {
		t.Fatalf("got %q, want a sha256= value", sig)
	}
	if sig != Signature("s3cret", body) {
		t.Fatal("the same body and secret must produce the same signature")
	}
	if sig == Signature("other", body) {
		t.Fatal("a different secret produced the same signature")
	}
	if sig == Signature("s3cret", append(body, ' ')) {
		t.Fatal("a different body produced the same signature")
	}
}

// TestAuthorizerSignsRequestsOnlyWhenASecretIsSet pins both halves of the
// switch: unsigned by default (every deployment before the option existed), and
// signed with the header the receiver verifies when a secret is configured.
func TestAuthorizerSignsRequestsOnlyWhenASecretIsSet(t *testing.T) {
	for _, tt := range []struct {
		name       string
		secret     string
		wantHeader bool
	}{
		{name: "no secret", secret: "", wantHeader: false},
		{name: "secret configured", secret: "s3cret", wantHeader: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var body []byte
			var got string
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ = io.ReadAll(r.Body)
				got = r.Header.Get(SignatureHeader)
				_ = json.NewEncoder(w).Encode(authorizationv1.SubjectAccessReview{
					Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true},
				})
			})

			a := NewAuthorizer("test", srv.URL, time.Second, FailureModeNoOpinion).WithSecret(tt.secret)
			if _, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
				Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
				Verb:    authz.VerbRead, AgentName: "some-agent",
			}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !tt.wantHeader {
				if got != "" {
					t.Fatalf("an unconfigured endpoint sent %s: %q", SignatureHeader, got)
				}
				return
			}
			if got != Signature(tt.secret, body) {
				t.Fatalf("header %q does not verify against the body that was sent", got)
			}
		})
	}
}
