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

package cmdb

import (
	"context"
	"errors"
	"testing"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

type fakeChecker struct {
	calls   int
	allowed bool
	err     error
}

func (f *fakeChecker) CheckServicePermission(context.Context, string, string, PermissionAction) (bool, error) {
	f.calls++
	return f.allowed, f.err
}

func TestAuthorizerIgnoresUnhandledResourceKinds(t *testing.T) {
	checker := &fakeChecker{}
	a := NewAuthorizer(checker)

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u1"},
		Verb:     authz.VerbRead,
		Resource: authz.Resource{Kind: authz.ResourceKindAgent, Name: "some-agent"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion for a non-CMDB resource kind", decision)
	}
	if checker.calls != 0 {
		t.Errorf("CMDB should not be queried for a resource kind it doesn't handle, got %d calls", checker.calls)
	}
}

func TestAuthorizerDefersOnNonUserSubjects(t *testing.T) {
	checker := &fakeChecker{allowed: true}
	a := NewAuthorizer(checker)

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindGroup, GroupCode: "eks_ec2"},
		Verb:     authz.VerbRead,
		Resource: authz.Resource{Kind: authz.ResourceKindService, Name: "payment-service"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion for a non-user subject (CMDB permission model is per-user)", decision)
	}
	if checker.calls != 0 {
		t.Errorf("CMDB should not be queried for a group subject, got %d calls", checker.calls)
	}
}

func TestAuthorizerAllowsWhenTheCheckerAllows(t *testing.T) {
	a := NewAuthorizer(&fakeChecker{allowed: true})

	decision, reason, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:     authz.VerbRead,
		Resource: authz.Resource{Kind: authz.ResourceKindService, Name: "payment-service"},
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

func TestAuthorizerDeniesWhenTheCheckerDenies(t *testing.T) {
	a := NewAuthorizer(&fakeChecker{allowed: false})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"},
		Verb:     authz.VerbWrite,
		Resource: authz.Resource{Kind: authz.ResourceKindService, Name: "payment-service"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionDeny {
		t.Fatalf("got %v, want Deny", decision)
	}
}

func TestAuthorizerReportsACheckerErrorAsNoOpinion(t *testing.T) {
	a := NewAuthorizer(&fakeChecker{err: errors.New("cmdb unreachable")})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:     authz.VerbRead,
		Resource: authz.Resource{Kind: authz.ResourceKindService, Name: "payment-service"},
	})
	if err == nil {
		t.Fatal("expected an error to propagate from the CMDB client failure")
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion on execution error (not a business Deny)", decision)
	}
}

func TestPlaceholderCheckerReportsNotImplemented(t *testing.T) {
	a := NewAuthorizer(NotImplementedChecker{})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:     authz.VerbRead,
		Resource: authz.Resource{Kind: authz.ResourceKindService, Name: "payment-service"},
	})
	if !errors.Is(err, ErrPermissionCheckNotImplemented) {
		t.Fatalf("got err %v, want ErrPermissionCheckNotImplemented", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion so other backends can still decide", decision)
	}
}
