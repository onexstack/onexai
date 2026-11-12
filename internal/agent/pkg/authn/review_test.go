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
	"net/http"
	"net/http/httptest"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/onexstack/onexai/internal/agent/model"
)

func TestTokenReviewReturnsTheWebhookIdentity(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var review authenticationv1.TokenReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Fatalf("decode: %v", err)
		}
		gotToken = review.Spec.Token
		_ = json.NewEncoder(w).Encode(authenticationv1.TokenReview{
			Status: authenticationv1.TokenReviewStatus{
				Authenticated: true,
				User: authenticationv1.UserInfo{
					Username: "alice",
					Groups:   []string{"oncall"},
				},
			},
		})
	}))
	defer server.Close()

	caller := NewReviewCaller(nil)
	status, outcome, err := caller.TokenReview(context.Background(), &model.WebhookM{
		Name: "sso",
		URL:  server.URL,
	}, authenticationv1.TokenReviewSpec{Token: "good", Audiences: []string{"onexai"}})
	if err != nil {
		t.Fatalf("TokenReview: %v", err)
	}
	if !status.Authenticated || status.User.Username != "alice" {
		t.Fatalf("status = %+v, want alice authenticated", status)
	}
	if gotToken != "good" {
		t.Fatalf("webhook saw token %q, want good", gotToken)
	}
	if outcome.HTTPStatus != http.StatusOK {
		t.Fatalf("http status = %d, want 200", outcome.HTTPStatus)
	}
}

func TestTokenReviewSendsTheSecretAsBearer(t *testing.T) {
	var gotAuth, gotTokenHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTokenHeader = r.Header.Get("X-Onexai-Token")
		_, _ = w.Write([]byte(`{"status":{"authenticated":false,"error":"nope"}}`))
	}))
	defer server.Close()

	caller := NewReviewCaller(func(context.Context, string) (string, bool) {
		return "s3cret", true
	})
	status, _, err := caller.TokenReview(context.Background(), &model.WebhookM{
		Name:      "sso",
		URL:       server.URL,
		SecretRef: "sso-secret",
	}, authenticationv1.TokenReviewSpec{Token: "bad"})
	if err != nil {
		t.Fatalf("TokenReview: %v", err)
	}
	if status.Authenticated {
		t.Fatal("the webhook refused the token, but the caller reported it authenticated")
	}
	if gotAuth != "Bearer s3cret" {
		t.Fatalf("Authorization = %q, want Bearer s3cret", gotAuth)
	}
	if gotTokenHeader != "s3cret" {
		t.Fatalf("X-Onexai-Token = %q, want s3cret", gotTokenHeader)
	}
}

func TestTokenReviewFailsOnNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	caller := NewReviewCaller(nil)
	if _, outcome, err := caller.TokenReview(context.Background(), &model.WebhookM{
		Name: "sso",
		URL:  server.URL,
	}, authenticationv1.TokenReviewSpec{Token: "good"}); err == nil {
		t.Fatal("a 500 must be a failed call, not an authentication decision")
	} else if outcome == nil || outcome.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("outcome = %+v, want the 500 reported", outcome)
	}
}

func TestTokenReviewFailsOnAnUnreadableBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()

	caller := NewReviewCaller(nil)
	if _, _, err := caller.TokenReview(context.Background(), &model.WebhookM{
		Name: "sso",
		URL:  server.URL,
	}, authenticationv1.TokenReviewSpec{Token: "good"}); err == nil {
		t.Fatal("an unparseable response must fail closed")
	}
}

func TestSubjectAccessReviewMapsTheStatus(t *testing.T) {
	cases := []struct {
		name   string
		status authorizationv1.SubjectAccessReviewStatus
	}{
		{name: "allow", status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}},
		{name: "deny", status: authorizationv1.SubjectAccessReviewStatus{Denied: true, Reason: "no"}},
		{name: "no opinion", status: authorizationv1.SubjectAccessReviewStatus{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(authorizationv1.SubjectAccessReview{Status: tc.status})
			}))
			defer server.Close()

			caller := NewReviewCaller(nil)
			status, _, err := caller.SubjectAccessReview(context.Background(), &model.WebhookM{
				Name: "policy",
				URL:  server.URL,
			}, authorizationv1.SubjectAccessReview{
				Spec: authorizationv1.SubjectAccessReviewSpec{User: "alice"},
			})
			if err != nil {
				t.Fatalf("SubjectAccessReview: %v", err)
			}
			if status.Allowed != tc.status.Allowed || status.Denied != tc.status.Denied {
				t.Fatalf("status = %+v, want %+v", status, tc.status)
			}
		})
	}
}
