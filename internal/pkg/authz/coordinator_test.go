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

func TestCoordinatorDisabledAllowsEverything(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionDeny),
		staticAuthorizer("resource", DecisionDeny),
		false, // Enabled=false
	)

	decision, _, err := c.AuthorizeAgent(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent")
	if err != nil || decision != DecisionAllow {
		t.Fatalf("disabled coordinator: got decision=%v err=%v, want Allow/nil", decision, err)
	}

	decision, _, err = c.AuthorizeToolCall(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, []string{"agent"}, "tool", "mcp", "{}", NoopResourceExtractor{})
	if err != nil || decision != DecisionAllow {
		t.Fatalf("disabled coordinator tool call: got decision=%v err=%v, want Allow/nil", decision, err)
	}
}

func TestCoordinatorAgentLevelDenyShortCircuits(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionDeny),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	decision, _, err := c.AuthorizeAgent(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionDeny {
		t.Fatalf("got %v, want Deny", decision)
	}
}

func TestCoordinatorToolLevelDenySkipsTheResourceCheck(t *testing.T) {
	ctx := context.Background()
	resourceCalled := false
	resourceAuthz := AuthorizerFunc(func(context.Context, AuthorizationRequest) (Decision, string, error) {
		resourceCalled = true
		return DecisionAllow, "", nil
	})

	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionDeny),
		resourceAuthz,
		true,
	)

	decision, _, err := c.AuthorizeToolCall(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, []string{"agent"}, "get-resource", "k8sgpt", `{"service":"svc-a"}`,
		&fakeExtractor{resource: Resource{Kind: ResourceKindService, Name: "svc-a"}, verb: VerbRead})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionDeny {
		t.Fatalf("got %v, want Deny", decision)
	}
	if resourceCalled {
		t.Error("resource authorizer should not be consulted once tool-level denies")
	}
}

func TestCoordinatorSkipsResourceLevelWithoutAResourceMapping(t *testing.T) {
	ctx := context.Background()
	resourceCalled := false
	resourceAuthz := AuthorizerFunc(func(context.Context, AuthorizationRequest) (Decision, string, error) {
		resourceCalled = true
		return DecisionDeny, "should never be reached", nil
	})

	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionAllow),
		resourceAuthz,
		true,
	)

	// NoopResourceExtractor{} simulates "no mapping configured" -> Resource is always empty.
	decision, _, err := c.AuthorizeToolCall(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, []string{"agent"}, "no-resource-tool", "mcp", `{}`, NoopResourceExtractor{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
	if resourceCalled {
		t.Error("resource authorizer must not be consulted when the tool has no resource mapping")
	}
}

func TestCoordinatorResourceLevelDenyOverridesToolAllow(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionAllow),
		staticAuthorizer("resource", DecisionDeny),
		true,
	)

	decision, _, err := c.AuthorizeToolCall(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, []string{"agent"}, "cmdb_search", "cmdb-mcp", `{"service":"svc-a"}`,
		&fakeExtractor{resource: Resource{Kind: ResourceKindService, Name: "svc-a"}, verb: VerbRead})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionDeny {
		t.Fatalf("got %v, want Deny: a user may be allowed to invoke a tool but still lack permission on the specific resource it targets", decision)
	}
}

func TestCoordinatorAllNoOpinionFailsClosed(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionNoOpinion),
		staticAuthorizer("resource", DecisionNoOpinion),
		true,
	)

	decision, _, err := c.AuthorizeToolCall(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, []string{"agent"}, "tool", "mcp", `{"service":"svc-a"}`,
		&fakeExtractor{resource: Resource{Kind: ResourceKindService, Name: "svc-a"}, verb: VerbRead})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionDeny {
		t.Fatalf("got %v, want Deny (fail-closed default)", decision)
	}
}

// There is no fail-open mode: an Authorizer that errors out ends the request as
// Deny and the error is propagated. A backend outage must never grant access.
func TestCoordinatorFailClosedPropagatesAnAuthorizerError(t *testing.T) {
	ctx := context.Background()
	erroringToolAuthz := erroringAuthorizer("tool", context.DeadlineExceeded)

	c := NewCoordinator(
		erroringToolAuthz,
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	decision, _, err := c.AuthorizeToolCall(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, []string{"agent"}, "tool", "mcp", `{}`, NoopResourceExtractor{})
	if err == nil {
		t.Fatal("expected an error to propagate under strict fail-closed")
	}
	if decision != DecisionDeny {
		t.Fatalf("got %v, want Deny", decision)
	}
}

func TestCoordinatorAgentBackendSelectionUsesThePerAgentSubset(t *testing.T) {
	ctx := context.Background()
	// Process-wide default denies everything; two distinct backends registered
	// so different Agents can each pick the one that allows them.
	c := NewCoordinator(
		staticAuthorizer("default", DecisionDeny),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	registry := NewRegistry(staticAuthorizer("default", DecisionDeny))
	registry.Register("opa", staticAuthorizer("opa", DecisionAllow))
	registry.Register("webhook:approval-desk", staticAuthorizer("webhook", DecisionDeny))

	backends := map[string][]string{
		"agent-a": {"opa"},
		"agent-b": {"webhook:approval-desk"},
	}
	c.WithAgentBackendSelection(registry, func(agentName string) ([]string, bool) {
		b, ok := backends[agentName]
		return b, ok
	})

	subj := Subject{Kind: SubjectKindUser, UserID: "u1"}

	decisionA, _, err := c.AuthorizeAgent(ctx, subj, "agent-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decisionA != DecisionAllow {
		t.Fatalf("agent-a: got %v, want Allow (via opa backend)", decisionA)
	}

	decisionB, _, err := c.AuthorizeAgent(ctx, subj, "agent-b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decisionB != DecisionDeny {
		t.Fatalf("agent-b: got %v, want Deny (via webhook:approval-desk backend)", decisionB)
	}
}

func TestCoordinatorAgentBackendSelectionNoneDisablesAuthorization(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("default", DecisionDeny),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	registry := NewRegistry(staticAuthorizer("default", DecisionDeny))
	c.WithAgentBackendSelection(registry, func(agentName string) ([]string, bool) {
		return []string{"none"}, true
	})

	decision, _, err := c.AuthorizeAgent(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "no-auth-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow when authz_backends=[\"none\"]", decision)
	}
}

func TestCoordinatorAgentBackendSelectionFallsBackWhenTheLookupMisses(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("default", DecisionAllow),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	registry := NewRegistry(staticAuthorizer("default", DecisionAllow))
	c.WithAgentBackendSelection(registry, func(agentName string) ([]string, bool) {
		return nil, false // no override configured for this agent
	})

	decision, _, err := c.AuthorizeAgent(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "plain-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow (falls back to process-wide agentAuthz)", decision)
	}
}

func TestCoordinatorAgentBackendSelectionKeepsAgentsIndependent(t *testing.T) {
	// Simulates the Agent-level authorization loop over multiple requested
	// agentNames: each call must be decided using its own Agent's configured
	// backend, never leaking into or being overridden by another Agent's.
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("default", DecisionAllow),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	registry := NewRegistry(staticAuthorizer("default", DecisionAllow))
	registry.Register("deny-backend", staticAuthorizer("deny-backend", DecisionDeny))
	registry.Register("allow-backend", staticAuthorizer("allow-backend", DecisionAllow))

	backends := map[string][]string{
		"agent-deny":  {"deny-backend"},
		"agent-allow": {"allow-backend"},
	}
	c.WithAgentBackendSelection(registry, func(agentName string) ([]string, bool) {
		b, ok := backends[agentName]
		return b, ok
	})

	subj := Subject{Kind: SubjectKindUser, UserID: "u1"}
	agentNames := []string{"agent-deny", "agent-allow", "agent-deny", "agent-allow"}
	for _, name := range agentNames {
		decision, _, err := c.AuthorizeAgent(ctx, subj, name)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", name, err)
		}
		want := DecisionAllow
		if name == "agent-deny" {
			want = DecisionDeny
		}
		if decision != want {
			t.Fatalf("%s: got %v, want %v — one Agent's backend must not affect another's", name, decision, want)
		}
	}
}

func TestCoordinatorNamespaceDenyOverridesAnAgentAllow(t *testing.T) {
	ctx := context.Background()
	// Agent-level and Namespace-level checks share the same resolved
	// authorizer, so this fake inspects the request's Resource.Kind to answer
	// differently per dimension.
	dimensionAware := AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) (Decision, string, error) {
		if req.Resource.Kind == ResourceKindNamespace {
			return DecisionDeny, "namespace denied", nil
		}
		return DecisionAllow, "agent allowed", nil
	})
	c := NewCoordinator(
		dimensionAware,
		staticAuthorizer("resource", DecisionAllow),
		true,
	)
	c.WithNamespaceAuthz(func(kind ResourceKind, name string) (string, bool) {
		return "team-a", true
	})

	decision, _, err := c.AuthorizeAgent(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionDeny {
		t.Fatalf("got %v, want Deny: agent-level allow must not override a namespace-level deny", decision)
	}
}

func TestCoordinatorWithoutNamespaceAuthzKeepsTheAgentLevelOnly(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionAllow),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)
	// WithNamespaceAuthz never called — must behave exactly like the
	// Agent-level-only wiring oai-agent-apiserver ships with.

	decision, _, err := c.AuthorizeAgent(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
}

func TestCoordinatorNamespaceLookupMissSkipsTheNamespaceLevel(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionAllow),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)
	c.WithNamespaceAuthz(func(kind ResourceKind, name string) (string, bool) {
		return "", false // this Agent has no known namespace
	})

	decision, _, err := c.AuthorizeAgent(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
}

func TestCoordinatorNamespaceAllowCarriesWhenTheAgentLevelHasNoOpinion(t *testing.T) {
	ctx := context.Background()
	dimensionAware := AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) (Decision, string, error) {
		if req.Resource.Kind == ResourceKindNamespace {
			return DecisionAllow, "namespace allowed", nil
		}
		return DecisionNoOpinion, "", nil
	})
	c := NewCoordinator(
		dimensionAware,
		staticAuthorizer("resource", DecisionAllow),
		true,
	)
	c.WithNamespaceAuthz(func(kind ResourceKind, name string) (string, bool) {
		return "team-a", true
	})

	decision, _, err := c.AuthorizeAgent(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow: agent-level NoOpinion should not block a namespace-level Allow", decision)
	}
}

func TestAuthorizeResourceRefsFailsClosedOnAllNoOpinion(t *testing.T) {
	ctx := context.Background()
	// No rule opines on any resource name. Like every other dimension, this
	// denies: hack/authz-rules-seed.sql lists the rows an Agent needs, and a
	// silent allow here is how a missing rule used to look like a working grant.
	c := NewCoordinator(
		staticAuthorizer("agentTool", DecisionNoOpinion),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	decision, reason, err := c.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent", ResourceKindSkill, []string{"sre-skills", "other-skill"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionDeny {
		t.Fatalf("got %v, want Deny (fail-closed on all-NoOpinion)", decision)
	}
	if reason == "" {
		t.Error("expected a reason naming the dimension that denied")
	}
}

func TestAuthorizeResourceRefsLetsASingleDenyDenyTheWholeBatch(t *testing.T) {
	ctx := context.Background()
	perNameAuthz := AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) (Decision, string, error) {
		if req.Resource.Name == "dangerous-skill" {
			return DecisionDeny, "explicitly blocked", nil
		}
		return DecisionNoOpinion, "", nil
	})
	c := NewCoordinator(perNameAuthz, staticAuthorizer("resource", DecisionAllow), true)

	decision, reason, err := c.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent",
		ResourceKindSkill, []string{"safe-skill", "dangerous-skill", "another-skill"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionDeny {
		t.Fatalf("got %v, want Deny", decision)
	}
	if reason == "" {
		t.Error("expected a non-empty deny reason")
	}
}

func TestAuthorizeResourceRefsLetsAnExplicitAllowWin(t *testing.T) {
	ctx := context.Background()
	perNameAuthz := AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) (Decision, string, error) {
		if req.Resource.Name == "sre-knowledgebase" {
			return DecisionAllow, "explicitly allowed", nil
		}
		return DecisionNoOpinion, "", nil
	})
	c := NewCoordinator(perNameAuthz, staticAuthorizer("resource", DecisionAllow), true)

	decision, _, err := c.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent",
		ResourceKindKnowledgeBase, []string{"sre-knowledgebase", "unrelated-kb"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
}

func TestAuthorizeResourceRefsAllowsWhenDisabledOrEmpty(t *testing.T) {
	ctx := context.Background()
	disabled := NewCoordinator(staticAuthorizer("agentTool", DecisionDeny), staticAuthorizer("resource", DecisionDeny), false)
	if decision, _, err := disabled.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent", ResourceKindMCP, []string{"some-mcp-server"}); err != nil || decision != DecisionAllow {
		t.Fatalf("disabled coordinator: got decision=%v err=%v, want Allow/nil", decision, err)
	}

	enabled := NewCoordinator(staticAuthorizer("agentTool", DecisionDeny), staticAuthorizer("resource", DecisionAllow), true)
	if decision, _, err := enabled.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent", ResourceKindMCP, nil); err != nil || decision != DecisionAllow {
		t.Fatalf("no resources to check: got decision=%v err=%v, want Allow/nil", decision, err)
	}
}

func TestAuthorizeResourceRefsUsesPerAgentBackendSelection(t *testing.T) {
	ctx := context.Background()
	c := NewCoordinator(staticAuthorizer("default", DecisionDeny), staticAuthorizer("resource", DecisionAllow), true)

	registry := NewRegistry(staticAuthorizer("default", DecisionDeny))
	registry.Register("opa", staticAuthorizer("opa", DecisionAllow))
	c.WithAgentBackendSelection(registry, func(agentName string) ([]string, bool) {
		if agentName == "agent-a" {
			return []string{"opa"}, true
		}
		return nil, false
	})

	decisionA, _, err := c.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "agent-a", ResourceKindSkill, []string{"some-skill"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decisionA != DecisionAllow {
		t.Fatalf("agent-a: got %v, want Allow (via opa backend)", decisionA)
	}

	decisionOther, _, err := c.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "agent-other", ResourceKindSkill, []string{"some-skill"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decisionOther != DecisionDeny {
		t.Fatalf("agent-other: got %v, want Deny (via process-wide default)", decisionOther)
	}
}

// TestAuthorizeResourceRefsIsFailClosedWhetherOrNotAUnionIsInFront pins the
// property the opt-out model used to hide: a bare authorizer that returns
// NoOpinion and the same authorizer behind the Union every deployment wires
// both deny. There is one default in this package, applied in one place.
func TestAuthorizeResourceRefsIsFailClosedWhetherOrNotAUnionIsInFront(t *testing.T) {
	ctx := context.Background()
	noOpinion := staticAuthorizer("opa", DecisionNoOpinion)

	t.Run("bare authorizer", func(t *testing.T) {
		c := NewCoordinator(noOpinion, noOpinion, true)
		decision, _, err := c.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"},
			"some-agent", ResourceKindMCP, []string{"some-mcp"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decision != DecisionDeny {
			t.Fatalf("got %v, want Deny", decision)
		}
	})

	t.Run("union-backed authorizer", func(t *testing.T) {
		unioned := NewUnionAuthorizer(noOpinion)
		c := NewCoordinator(unioned, unioned, true)
		decision, reason, err := c.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"},
			"some-agent", ResourceKindMCP, []string{"some-mcp"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decision != DecisionDeny {
			t.Fatalf("got %v, want Deny", decision)
		}
		if reason == "" {
			t.Error("expected a reason naming the dimension that denied")
		}
	})
}

type fakeExtractor struct {
	resource Resource
	verb     Verb
	err      error
}

func (f *fakeExtractor) Extract(context.Context, string, string, string) (Resource, Verb, error) {
	if f.err != nil {
		return Resource{Kind: ResourceKindNone}, "", f.err
	}
	return f.resource, f.verb, nil
}

func TestCoordinatorNamespaceCheckIssuedForResourceRefs(t *testing.T) {
	ctx := context.Background()
	var sawNamespace bool
	authz := AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) (Decision, string, error) {
		if req.Resource.Kind == ResourceKindNamespace {
			sawNamespace = true
			return DecisionDeny, "namespace refused", nil
		}
		return DecisionAllow, "", nil
	})
	c := NewCoordinator(authz, staticAuthorizer("resource", DecisionAllow), true)
	c.WithNamespaceAuthz(func(kind ResourceKind, name string) (string, bool) {
		if kind == ResourceKindSkill {
			return "team-a", true
		}
		return "", false
	})

	decision, _, err := c.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent", ResourceKindSkill, []string{"sre-skills"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawNamespace {
		t.Fatal("expected a namespace-level check to be issued for a skill reference")
	}
	if decision != DecisionDeny {
		t.Fatalf("got %v, want Deny: a namespace denial must not be overridden by the resource-level allow", decision)
	}
}

func TestCoordinatorNamespaceLookupMissOnResourceRefsSkipsTheNamespaceCheck(t *testing.T) {
	ctx := context.Background()
	var sawNamespace bool
	authz := AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) (Decision, string, error) {
		if req.Resource.Kind == ResourceKindNamespace {
			sawNamespace = true
		}
		return DecisionAllow, "", nil
	})
	c := NewCoordinator(authz, staticAuthorizer("resource", DecisionAllow), true)
	c.WithNamespaceAuthz(func(kind ResourceKind, name string) (string, bool) {
		return "", false // no namespace known for this resource
	})

	decision, _, err := c.AuthorizeResourceRefs(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "some-agent", ResourceKindSkill, []string{"sre-skills"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawNamespace {
		t.Fatal("no namespace check must be issued when the resource has no known namespace")
	}
	if decision != DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
}

// TestAuthorizeUseCombinesSubjects pins the multi-subject algebra a service
// needs when its caller is "a user plus the groups they belong to": one Allow is
// enough, any Deny ends the request, and a caller that names no subject is
// refused rather than waved through.
func TestAuthorizeUseCombinesSubjects(t *testing.T) {
	ctx := context.Background()
	user := Subject{Kind: SubjectKindUser, UserID: "u1"}
	sre := Subject{Kind: SubjectKindGroup, GroupCode: "sre"}

	perSubject := func(denyUser, allowGroup bool) Authorizer {
		return AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) (Decision, string, error) {
			switch req.Subject.Kind {
			case SubjectKindUser:
				if denyUser {
					return DecisionDeny, "user is denied", nil
				}
				return DecisionNoOpinion, "", nil
			case SubjectKindGroup:
				if allowGroup {
					return DecisionAllow, "group grants it", nil
				}
				return DecisionNoOpinion, "", nil
			}
			return DecisionNoOpinion, "", nil
		})
	}

	tests := []struct {
		name     string
		authz    Authorizer
		subjects []Subject
		want     Decision
	}{
		{
			name:     "group Allow is enough",
			authz:    perSubject(false, true),
			subjects: []Subject{user, sre},
			want:     DecisionAllow,
		},
		{
			name:     "a Deny on any subject wins over another subject's Allow",
			authz:    perSubject(true, true),
			subjects: []Subject{user, sre},
			want:     DecisionDeny,
		},
		{
			name:     "all NoOpinion fails closed",
			authz:    perSubject(false, false),
			subjects: []Subject{user, sre},
			want:     DecisionDeny,
		},
		{
			name:     "no subjects is a denial, not a pass",
			authz:    perSubject(false, true),
			subjects: nil,
			want:     DecisionDeny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCoordinator(tt.authz, staticAuthorizer("resource", DecisionAllow), true)
			decision, _, err := c.AuthorizeUse(ctx, tt.subjects, VerbRead, ResourceKindRole, "sre")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decision != tt.want {
				t.Fatalf("got %v, want %v", decision, tt.want)
			}
		})
	}
}

// TestAuthorizeUseIsDisabledFriendly pins the switch: a nil or disabled
// coordinator allows, exactly like every other entry point, so a service can
// call it unconditionally and keep its old behaviour when authz is off.
func TestAuthorizeUseIsDisabledFriendly(t *testing.T) {
	var nilCoordinator *Coordinator
	if decision, _, _ := nilCoordinator.AuthorizeUse(context.Background(), nil, VerbRead, ResourceKindRole, "sre"); decision != DecisionAllow {
		t.Fatalf("nil coordinator: got %v, want Allow", decision)
	}
	disabled := NewCoordinator(staticAuthorizer("agentTool", DecisionDeny), staticAuthorizer("resource", DecisionDeny), false)
	if decision, _, _ := disabled.AuthorizeUse(context.Background(), []Subject{{Kind: SubjectKindUser, UserID: "u1"}}, VerbRead, ResourceKindRole, "sre"); decision != DecisionAllow {
		t.Fatalf("disabled coordinator: got %v, want Allow", decision)
	}
}

// A qualified agent reference names its namespace directly, while the rules
// stay keyed by the bare agent name. Passing the qualified string through to
// the policy engine would silence every rule written for that agent.
func TestAuthorizeAgentSplitsAQualifiedReference(t *testing.T) {
	ctx := context.Background()
	var seen []AuthorizationRequest
	c := NewCoordinator(
		AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) (Decision, string, error) {
			seen = append(seen, req)
			return DecisionAllow, "allowed", nil
		}),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	decision, _, err := c.AuthorizeAgent(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"}, "team-a/helper")
	if err != nil || decision != DecisionAllow {
		t.Fatalf("AuthorizeAgent: decision=%v err=%v", decision, err)
	}
	if len(seen) != 2 {
		t.Fatalf("checks = %d, want namespace and agent", len(seen))
	}
	if seen[0].Resource.Kind != ResourceKindNamespace || seen[0].Resource.Name != "team-a" {
		t.Fatalf("namespace check = %+v, want team-a", seen[0].Resource)
	}
	if seen[1].AgentName != "helper" || seen[1].AgentNames != nil {
		t.Fatalf("agent check = AgentName %q, want the bare name", seen[1].AgentName)
	}
}

// Tool-call rules match on the bare agent names even when the run was started
// with qualified references.
func TestAuthorizeToolCallUsesBareAgentNames(t *testing.T) {
	ctx := context.Background()
	var seen []AuthorizationRequest
	c := NewCoordinator(
		AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) (Decision, string, error) {
			seen = append(seen, req)
			return DecisionAllow, "allowed", nil
		}),
		staticAuthorizer("resource", DecisionAllow),
		true,
	)

	decision, _, err := c.AuthorizeToolCall(ctx, Subject{Kind: SubjectKindUser, UserID: "u1"},
		[]string{"team-a/helper"}, "tool", "mcp", "{}", NoopResourceExtractor{})
	if err != nil || decision != DecisionAllow {
		t.Fatalf("AuthorizeToolCall: decision=%v err=%v", decision, err)
	}
	if len(seen) == 0 {
		t.Fatal("no authorization checks ran")
	}
	if got := seen[0].AgentNames; len(got) != 1 || got[0] != "helper" {
		t.Fatalf("AgentNames = %v, want the bare name", got)
	}
}
