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
	"sync/atomic"
	"testing"
	"time"
)

// countingAuthorizer records how many times Authorize was called, returning a
// fixed decision/error.
type countingAuthorizer struct {
	AuthorizerFunc
	calls *atomic.Int32
}

func newCountingAuthorizer(decision Decision, err error) *countingAuthorizer {
	c := &countingAuthorizer{calls: new(atomic.Int32)}
	c.AuthorizerFunc = func(_ context.Context, _ AuthorizationRequest) (Decision, string, error) {
		c.calls.Add(1)
		return decision, "counted", err
	}
	return c
}

func (c *countingAuthorizer) Name() string { return "counting" }

func TestCachedAuthorizerCachesPositiveResult(t *testing.T) {
	inner := newCountingAuthorizer(DecisionAllow, nil)
	cached, err := NewCachedAuthorizer(inner, time.Minute)
	if err != nil {
		t.Fatalf("NewCachedAuthorizer: %v", err)
	}

	req := AuthorizationRequest{
		Subject:   Subject{Kind: SubjectKindUser, UserID: "u_alice"},
		Verb:      VerbRead,
		AgentName: "sre-agent",
	}

	// First call: cache miss, hits inner. Subsequent calls hit the cache.
	for i := 0; i < 3; i++ {
		decision, _, e := cached.Authorize(context.Background(), req)
		if e != nil {
			t.Fatalf("unexpected error: %v", e)
		}
		if decision != DecisionAllow {
			t.Fatalf("got %v, want Allow", decision)
		}
		cached.Wait() // ristretto writes are async — wait before next read
	}
	if inner.calls.Load() != 1 {
		t.Fatalf("got %d inner calls, want 1 (subsequent calls should hit the cache)", inner.calls.Load())
	}
}

func TestCachedAuthorizerDoesNotCacheErrors(t *testing.T) {
	inner := newCountingAuthorizer(DecisionNoOpinion, errors.New("backend down"))
	cached, err := NewCachedAuthorizer(inner, time.Minute)
	if err != nil {
		t.Fatalf("NewCachedAuthorizer: %v", err)
	}

	req := AuthorizationRequest{
		Subject:   Subject{Kind: SubjectKindUser, UserID: "u_alice"},
		Verb:      VerbRead,
		AgentName: "sre-agent",
	}

	// Each call must hit the inner authorizer — errors are never cached.
	for i := 0; i < 3; i++ {
		_, _, e := cached.Authorize(context.Background(), req)
		if e == nil {
			t.Fatal("expected an error to propagate")
		}
	}
	if inner.calls.Load() != 3 {
		t.Fatalf("got %d inner calls, want 3 (errors must not be cached)", inner.calls.Load())
	}
}

func TestCachedAuthorizerKeysOnSubjectVerbAndExtra(t *testing.T) {
	inner := newCountingAuthorizer(DecisionAllow, nil)
	cached, err := NewCachedAuthorizer(inner, time.Minute)
	if err != nil {
		t.Fatalf("NewCachedAuthorizer: %v", err)
	}

	base := AuthorizationRequest{
		Subject:   Subject{Kind: SubjectKindUser, UserID: "u_alice"},
		Verb:      VerbRead,
		AgentName: "sre-agent",
	}

	alice := base

	bob := base
	bob.Subject = Subject{Kind: SubjectKindUser, UserID: "u_bob"}

	write := base
	write.Verb = VerbWrite

	groupA := base
	groupA.Subject = Subject{Kind: SubjectKindUser, UserID: "u_alice", Extra: map[string]string{"oncall_group_codes": "sre-oncall"}}

	groupB := base
	groupB.Subject = Subject{Kind: SubjectKindUser, UserID: "u_alice", Extra: map[string]string{"oncall_group_codes": "dba-oncall"}}

	for _, req := range []AuthorizationRequest{alice, bob, write, groupA, groupB} {
		if _, _, e := cached.Authorize(context.Background(), req); e != nil {
			t.Fatalf("unexpected error: %v", e)
		}
	}
	if inner.calls.Load() != 5 {
		t.Fatalf("got %d inner calls, want 5 (subject, verb and Extra must all participate in the key)", inner.calls.Load())
	}
}

func TestCachedAuthorizerPreservesInnerName(t *testing.T) {
	inner := AuthorizerFunc(func(context.Context, AuthorizationRequest) (Decision, string, error) {
		return DecisionAllow, "", nil
	})
	cached, err := NewCachedAuthorizer(inner, time.Minute)
	if err != nil {
		t.Fatalf("NewCachedAuthorizer: %v", err)
	}
	if cached.Name() != "cached:func" {
		t.Fatalf("got %q, want %q", cached.Name(), "cached:func")
	}
}
