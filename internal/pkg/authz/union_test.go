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

package authz

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// staticAuthorizer returns an Authorizer that always answers decision with a
// stable "<name> reason".
func staticAuthorizer(name string, decision Decision) Authorizer {
	return &namedAuthorizerFunc{
		name: name,
		fn: func(context.Context, AuthorizationRequest) (Decision, string, error) {
			return decision, name + " reason", nil
		},
	}
}

// erroringAuthorizer returns an Authorizer whose Authorize always fails, to
// exercise the "backend execution error" mapping.
func erroringAuthorizer(name string, err error) Authorizer {
	return &namedAuthorizerFunc{
		name: name,
		fn: func(context.Context, AuthorizationRequest) (Decision, string, error) {
			return DecisionNoOpinion, "", err
		},
	}
}

type namedAuthorizerFunc struct {
	name string
	fn   func(context.Context, AuthorizationRequest) (Decision, string, error)
}

func (n *namedAuthorizerFunc) Authorize(ctx context.Context, req AuthorizationRequest) (Decision, string, error) {
	return n.fn(ctx, req)
}
func (n *namedAuthorizerFunc) Name() string { return n.name }

func TestUnionAuthorizerCombinesDenyWinsAnyAllowPasses(t *testing.T) {
	ctx := context.Background()
	req := AuthorizationRequest{}

	tests := []struct {
		name        string
		authorizers []Authorizer
		want        Decision
	}{
		{
			name:        "all NoOpinion fails closed",
			authorizers: []Authorizer{staticAuthorizer("a", DecisionNoOpinion), staticAuthorizer("b", DecisionNoOpinion)},
			want:        DecisionDeny,
		},
		{
			name:        "single Allow wins",
			authorizers: []Authorizer{staticAuthorizer("a", DecisionNoOpinion), staticAuthorizer("b", DecisionAllow)},
			want:        DecisionAllow,
		},
		{
			name:        "Deny short-circuits before a later Allow",
			authorizers: []Authorizer{staticAuthorizer("a", DecisionDeny), staticAuthorizer("b", DecisionAllow)},
			want:        DecisionDeny,
		},
		{
			name:        "Deny after an Allow still wins (deny-wins semantics)",
			authorizers: []Authorizer{staticAuthorizer("a", DecisionAllow), staticAuthorizer("b", DecisionDeny)},
			want:        DecisionDeny,
		},
		{
			name:        "erroring authorizer treated as NoOpinion, later Allow still wins",
			authorizers: []Authorizer{erroringAuthorizer("a", errors.New("boom")), staticAuthorizer("b", DecisionAllow)},
			want:        DecisionAllow,
		},
		{
			name:        "erroring authorizer alone fails closed",
			authorizers: []Authorizer{erroringAuthorizer("a", errors.New("boom"))},
			want:        DecisionDeny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := NewUnionAuthorizer(tt.authorizers...)
			got, _, err := u.Authorize(ctx, req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnionAuthorizerWithoutAuthorizersFailsClosed(t *testing.T) {
	got, reason, err := NewUnionAuthorizer().Authorize(context.Background(), AuthorizationRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != DecisionDeny {
		t.Fatalf("got %v, want Deny for an empty Union", got)
	}
	if reason == "" {
		t.Fatal("want a reason explaining the fail-closed default, got empty")
	}
}

// TestUnionReasonNamesTheBackendNotTheWrapper pins what a person reads: a
// denial reason ends up on an approval card and in /v1/authz/decisions, where
// "cached:opa" is an implementation detail. The cache marker stays in Name()
// (metrics/logs) and is dropped from the reason via DisplayName.
func TestUnionReasonNamesTheBackendNotTheWrapper(t *testing.T) {
	cached, err := NewCachedAuthorizer(staticAuthorizer("opa", DecisionAllow), time.Minute)
	if err != nil {
		t.Fatalf("NewCachedAuthorizer: %v", err)
	}
	u := NewUnionAuthorizer(cached)

	decision, reason, err := u.Authorize(context.Background(), AuthorizationRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
	if strings.Contains(reason, "cached:") {
		t.Fatalf("reason %q exposes the cache wrapper; it should name the backend", reason)
	}
	if !strings.Contains(reason, "opa") {
		t.Fatalf("reason %q should name the backend that decided", reason)
	}
}
