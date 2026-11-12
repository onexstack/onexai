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
	"testing"
)

func TestRegistryResolveFallsBackToDefaultWithoutNames(t *testing.T) {
	def := staticAuthorizer("default", DecisionAllow)
	r := NewRegistry(def)

	got := r.Resolve("agent-a", nil)
	if got != def {
		t.Fatalf("expected default authorizer, got %v", got)
	}
}

func TestRegistryResolveNoneDisablesAuthorization(t *testing.T) {
	def := staticAuthorizer("default", DecisionDeny)
	r := NewRegistry(def)
	r.Register("opa", staticAuthorizer("opa", DecisionDeny))

	authorizer := r.Resolve("agent-a", []string{"none"})
	decision, _, err := authorizer.Authorize(context.Background(), AuthorizationRequest{AgentName: "agent-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow for authz_backends=[\"none\"]", decision)
	}
}

func TestRegistryResolveSkipsUnknownBackends(t *testing.T) {
	def := staticAuthorizer("default", DecisionDeny)
	r := NewRegistry(def)
	r.Register("opa", staticAuthorizer("opa", DecisionAllow))

	authorizer := r.Resolve("agent-a", []string{"opa", "does-not-exist"})
	decision, _, err := authorizer.Authorize(context.Background(), AuthorizationRequest{AgentName: "agent-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow from the one resolvable backend", decision)
	}
}

func TestRegistryResolveFallsBackWhenNoBackendResolves(t *testing.T) {
	def := staticAuthorizer("default", DecisionAllow)
	r := NewRegistry(def)
	r.Register("opa", staticAuthorizer("opa", DecisionDeny))

	got := r.Resolve("agent-a", []string{"does-not-exist"})
	if got != def {
		t.Fatalf("expected fallback to default authorizer when no backend resolves, got %v", got)
	}
}

func TestRegistrySetDefaultUpdatesFallback(t *testing.T) {
	r := NewRegistry(nil)
	newDefault := staticAuthorizer("new-default", DecisionAllow)
	r.SetDefault(newDefault)

	got := r.Resolve("agent-a", nil)
	if got != newDefault {
		t.Fatalf("expected updated default authorizer, got %v", got)
	}
}

func TestRegistryResolveCachesSameBackendSetRegardlessOfOrder(t *testing.T) {
	def := staticAuthorizer("default", DecisionDeny)
	r := NewRegistry(def)
	r.Register("opa", staticAuthorizer("opa", DecisionAllow))
	r.Register("cmdb", staticAuthorizer("cmdb", DecisionAllow))

	first := r.Resolve("agent-a", []string{"opa", "cmdb"})
	second := r.Resolve("agent-b", []string{"opa", "cmdb"})
	if first != second {
		t.Fatalf("expected the same backend set to resolve to the same cached Authorizer instance across different agents")
	}

	// Order-independence: same set, different order, should still hit the cache.
	third := r.Resolve("agent-c", []string{"cmdb", "opa"})
	if third != first {
		t.Fatalf("expected backend order to not affect the cache key")
	}
}

func TestRegistryResolveKeepsDifferentBackendSetsApart(t *testing.T) {
	def := staticAuthorizer("default", DecisionDeny)
	r := NewRegistry(def)
	r.Register("opa", staticAuthorizer("opa", DecisionAllow))
	r.Register("cmdb", staticAuthorizer("cmdb", DecisionDeny))

	opaOnly := r.Resolve("agent-a", []string{"opa"})
	cmdbOnly := r.Resolve("agent-b", []string{"cmdb"})
	if opaOnly == cmdbOnly {
		t.Fatalf("expected distinct backend sets to resolve to distinct Authorizer instances")
	}
}

func TestRegistryNamesIsSortedAndExcludesNone(t *testing.T) {
	r := NewRegistry(staticAuthorizer("default", DecisionDeny))
	r.Register("opa", staticAuthorizer("opa", DecisionAllow))
	r.Register("cmdb", staticAuthorizer("cmdb", DecisionAllow))
	r.Register("webhook:approval-desk", staticAuthorizer("webhook", DecisionAllow))

	got := r.Names()
	want := []string{"cmdb", "opa", "webhook:approval-desk"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}
