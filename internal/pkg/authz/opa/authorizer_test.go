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

package opa

import (
	"context"
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// newTestAuthorizer builds an Authorizer from an in-memory rule set. Sync is
// not exercised here (see store_test.go for that); only Snapshot()'s consumer
// (Authorizer.Reload) is.
func newTestAuthorizer(t *testing.T, rules []model.AuthzRuleM) *Authorizer {
	t.Helper()
	rs := NewRuleStore(nil)
	rs.rules.Seed(rules) // white-box: set the snapshot directly, no loader needed.
	a, err := NewAuthorizer(context.Background(), rs)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	return a
}

const enabled = model.StatusEnabled

func TestAuthorizerAllowsAnAgentByOnCallGroup(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-001", SubjectType: "group", SubjectID: []string{"eks_ec2"},
			ResourceKind: "agent", ResourceName: []string{"k8s-sre-agent"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindGroup, GroupCode: "eks_ec2"},
		Verb:      authz.VerbRead,
		AgentName: "k8s-sre-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
}

// TestAuthorizerAllowsByServiceAccount pins the generic subject_matches branch:
// a serviceaccount rule matches by Subject.PrimaryID() with no per-kind policy
// special-casing.
func TestAuthorizerAllowsByServiceAccount(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-001", SubjectType: "serviceaccount", SubjectID: []string{"oai-chat-apiserver"},
			ResourceKind: "agent", ResourceName: []string{"k8s-sre-agent"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindServiceAccount, ServiceName: "oai-chat-apiserver"},
		Verb:      authz.VerbRead,
		AgentName: "k8s-sre-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow", decision)
	}
}

func TestAuthorizerDoesNotLeakAServiceAccountRuleOntoAnotherService(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-001", SubjectType: "serviceaccount", SubjectID: []string{"other-service"},
			ResourceKind: "agent", ResourceName: []string{"k8s-sre-agent"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindServiceAccount, ServiceName: "oai-chat-apiserver"},
		Verb:      authz.VerbRead,
		AgentName: "k8s-sre-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion", decision)
	}
}

func TestAuthorizerHasNoOpinionWithoutAMatchingRule(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-001", SubjectType: "user", SubjectID: []string{"u_alice"},
			ResourceKind: "agent", ResourceName: []string{"k8s-sre-agent"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"},
		Verb:      authz.VerbRead,
		AgentName: "k8s-sre-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion for a subject with no matching rule", decision)
	}
}

func TestAuthorizerLetsAnExplicitDenyOverrideAWildcardAllow(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-allow-all", SubjectType: "*", SubjectID: []string{"*"},
			ResourceKind: "agent", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
		{
			Name: "rule-deny-dangerous", SubjectType: "*", SubjectID: []string{"*"},
			ResourceKind: "agent", ResourceName: []string{"dangerous-agent"},
			Verbs: []string{"read"}, Effect: "deny", Status: enabled,
		},
	})

	decision, reason, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_anyone"},
		Verb:      authz.VerbRead,
		AgentName: "dangerous-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionDeny {
		t.Fatalf("got %v, want Deny (explicit deny must override the wildcard allow)", decision)
	}
	if reason == "" {
		t.Error("expected a non-empty reason for the deny")
	}
}

func TestAuthorizerMatchesAnyToolViaAWildcardResourceName(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-mcp-wide", SubjectType: "user", SubjectID: []string{"u_alice"},
			ResourceKind: "tool", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:     authz.VerbRead,
		ToolName: "get-resource",
		MCPName:  "k8sgpt",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow via wildcard resource_name", decision)
	}
}

func TestAuthorizerHasNoOpinionWhenTheVerbIsNotGranted(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-read-only", SubjectType: "user", SubjectID: []string{"u_alice"},
			ResourceKind: "tool", ResourceName: []string{"get-resource"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:     authz.VerbWrite,
		ToolName: "get-resource",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion when the rule's verbs don't include the request's verb", decision)
	}
}

func TestAuthorizerLetsAUserInheritAGroupGrant(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-group", SubjectType: "group", SubjectID: []string{"eks_ec2"},
			ResourceKind: "tool", ResourceName: []string{"get-resource"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject: authz.Subject{
			Kind: authz.SubjectKindUser, UserID: "u_alice",
			Extra: map[string]string{"oncall_group_codes": "eks_ec2"},
		},
		Verb:     authz.VerbRead,
		ToolName: "get-resource",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow: a user on duty for eks_ec2 should inherit the group's tool grant", decision)
	}
}

func TestAuthorizerLetsAUserInheritANamespaceGrantThroughItsGroup(t *testing.T) {
	// The rule is written once against the namespace, not against any specific
	// Group code — the "namespace -> Group -> User" inheritance path.
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-namespace", SubjectType: "namespace", SubjectID: []string{"team-a"},
			ResourceKind: "agent", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject: authz.Subject{
			Kind: authz.SubjectKindUser, UserID: "u_alice",
			Extra: map[string]string{"oncall_group_codes": "team-a-oncall", "group_namespace": "team-a"},
		},
		Verb:      authz.VerbRead,
		AgentName: "any-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow: a user whose duty group belongs to namespace team-a should inherit its grant", decision)
	}
}

func TestAuthorizerDoesNotMatchANamespaceRuleWithoutAGroupNamespace(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-namespace", SubjectType: "namespace", SubjectID: []string{"team-a"},
			ResourceKind: "agent", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"}, // no group_namespace at all
		Verb:      authz.VerbRead,
		AgentName: "any-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion: a user with no group_namespace must not match a namespace-scoped rule", decision)
	}
}

func TestAuthorizerMatchesANamespaceResourceDirectly(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-workspace-resource", SubjectType: "user", SubjectID: []string{"u_alice"},
			ResourceKind: "namespace", ResourceName: []string{"team-a"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:      authz.VerbRead,
		AgentName: "some-agent",
		Resource:  authz.Resource{Kind: authz.ResourceKindNamespace, Name: "team-a"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow: resource_kind=namespace should match like any other resource_kind", decision)
	}
}

func TestAuthorizerHasNoOpinionWithNoRulesLoaded(t *testing.T) {
	a := newTestAuthorizer(t, nil)

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:      authz.VerbRead,
		AgentName: "some-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion with no rules loaded", decision)
	}
}

func TestAuthorizerReloadPicksUpNewRules(t *testing.T) {
	rs := NewRuleStore(nil)
	a, err := NewAuthorizer(context.Background(), rs)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	req := authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:      authz.VerbRead,
		AgentName: "new-agent",
	}

	decision, _, err := a.Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion before any rule is loaded", decision)
	}

	rs.rules.Seed([]model.AuthzRuleM{
		{
			Name: "rule-new", SubjectType: "user", SubjectID: []string{"u_alice"},
			ResourceKind: "agent", ResourceName: []string{"new-agent"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})
	if err := a.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	decision, _, err = a.Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow after Reload picks up the new rule", decision)
	}
}

// ── resource_attributes ──

// attrRule builds a wildcard rule carrying a resource_attributes filter.
func attrRule(attrs, effect string) model.AuthzRuleM {
	return model.AuthzRuleM{
		Name: "rule-attrs", SubjectType: "*", SubjectID: []string{"*"},
		ResourceKind: "*", ResourceName: []string{"*"},
		ResourceAttributes: attrs,
		Verbs:              []string{"read"}, Effect: effect, Status: enabled,
	}
}

func TestAuthorizerResourceAttributesMatchValues(t *testing.T) {
	tests := []struct {
		name  string
		attrs string
		input map[string]string
		want  authz.Decision
	}{
		{"exact match", `{"account_id":"111"}`, map[string]string{"account_id": "111"}, authz.DecisionAllow},
		{"glob match", `{"cluster_name":"test-*"}`, map[string]string{"cluster_name": "test-eks-01"}, authz.DecisionAllow},
		{"glob mismatch", `{"cluster_name":"test-*"}`, map[string]string{"cluster_name": "prod-cluster"}, authz.DecisionNoOpinion},
		{"array contains the value", `{"account_id":["111","222","333"]}`, map[string]string{"account_id": "222"}, authz.DecisionAllow},
		{"array misses the value", `{"account_id":["111","222"]}`, map[string]string{"account_id": "999"}, authz.DecisionNoOpinion},
		{"array of globs", `{"cluster_name":["test-*","dev-*"]}`, map[string]string{"cluster_name": "dev-cluster-hk"}, authz.DecisionAllow},
		{"mixed exact and glob array", `{"cluster_name":["prod-cluster-01","test-*"]}`, map[string]string{"cluster_name": "test-eks"}, authz.DecisionAllow},
		{"all keys match", `{"account_id":"111","cluster_name":["test-*","dev-*"]}`, map[string]string{"account_id": "111", "cluster_name": "test-eks"}, authz.DecisionAllow},
		{"one key mismatches", `{"account_id":"111","cluster_name":["test-*","dev-*"]}`, map[string]string{"account_id": "111", "cluster_name": "prod-cluster"}, authz.DecisionNoOpinion},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAuthorizer(t, []model.AuthzRuleM{attrRule(tt.attrs, "allow")})
			decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
				Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
				Verb:     authz.VerbRead,
				Resource: authz.Resource{Kind: authz.ResourceKindK8sNamespace, Name: "my-ns", Attributes: tt.input},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decision != tt.want {
				t.Fatalf("got %v, want %v", decision, tt.want)
			}
		})
	}
}

func TestAuthorizerAttributeRuleDoesNotMatchAnInputWithoutAttributes(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{attrRule(`{"account_id":"111"}`, "allow")})

	// No Resource at all (an agent-level request).
	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:   authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:      authz.VerbRead,
		AgentName: "some-agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v, want NoOpinion when a rule requires attrs but the input has none", decision)
	}
}

func TestAuthorizerLegacyRuleWithoutAttributesMatchesAnything(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{attrRule("", "allow")})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:     authz.VerbRead,
		Resource: authz.Resource{Kind: authz.ResourceKindK8sNamespace, Name: "my-ns", Attributes: map[string]string{"account_id": "111"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow for a legacy rule without resource_attributes", decision)
	}
}

func TestAuthorizerAttributeDenyOverridesAttributeAllow(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		attrRule(`{"cluster_name":["test-*","dev-*"]}`, "allow"),
		attrRule(`{"cluster_name":"test-dangerous"}`, "deny"),
	})

	decision, reason, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:     authz.VerbRead,
		Resource: authz.Resource{Kind: authz.ResourceKindK8sNamespace, Name: "my-ns", Attributes: map[string]string{"cluster_name": "test-dangerous"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionDeny {
		t.Fatalf("got %v, want Deny when a specific deny rule with attrs overrides the allow", decision)
	}
	if reason == "" {
		t.Error("expected a non-empty reason for the deny")
	}
}

// TestAuthorizerSurvivesSeveralRulesMatchingOneRequest pins the Rego policy's
// complete-rule discipline: `decision` has to produce exactly one output
// however many rules cover the request.
//
// # What this guards against, and why it is worth a test of its own
//
// The decision body used to bind `r` to each matching rule in turn, which made
// the body satisfiable once per match; two matches were two outputs, and OPA
// rejects that with
//
//	eval_conflict_error: complete rules must not produce multiple outputs
//
// Unlike a wrong answer, the failure is not local: the error propagates out of
// Authorize, the Union downgrades an execution error to NoOpinion (union.go),
// and a request that two rules grant is refused by the fail-closed default. The
// caller reads "authorization: all levels NoOpinion (fail-closed)" with no hint
// that the rules denying them are the ones that allow them. A workspace covered
// by both a wildcard grant and a platform's own grant is the everyday case.
//
// The single-rule cases are covered by the rest of this file and by
// baseline_seed_test.go; this is the multiplicity that only two matches expose.
func TestAuthorizerSurvivesSeveralRulesMatchingOneRequest(t *testing.T) {
	allow := func(name, resourceName string) model.AuthzRuleM {
		return model.AuthzRuleM{
			Name: name, SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "namespace", ResourceName: []string{resourceName},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		}
	}
	deny := func(name, resourceName string) model.AuthzRuleM {
		return model.AuthzRuleM{
			Name: name, SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "namespace", ResourceName: []string{resourceName},
			Verbs: []string{"read"}, Effect: "deny", Status: enabled,
		}
	}
	req := authz.AuthorizationRequest{
		Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "user-mkc1kj"},
		Verb:     authz.VerbRead,
		Resource: authz.Resource{Kind: authz.ResourceKindNamespace, Name: "onex"},
	}

	tests := []struct {
		name  string
		rules []model.AuthzRuleM
		want  authz.Decision
	}{
		{
			name:  "two allow rules match",
			rules: []model.AuthzRuleM{allow("grant-wildcard-workspace", "*"), allow("grant-onex-workspace", "onex")},
			want:  authz.DecisionAllow,
		},
		{
			name:  "three allow rules match",
			rules: []model.AuthzRuleM{allow("a-wildcard", "*"), allow("b-onex", "onex"), allow("c-also-onex", "onex")},
			want:  authz.DecisionAllow,
		},
		{
			// Deny-wins has to hold when the *deny* side is the multiple one:
			// a deny body that matched twice used to error out and be read as
			// NoOpinion, which let an allow rule through.
			name:  "two deny rules match",
			rules: []model.AuthzRuleM{deny("block-wildcard-workspace", "*"), deny("block-onex-workspace", "onex")},
			want:  authz.DecisionDeny,
		},
		{
			name:  "several allow rules and one deny rule match",
			rules: []model.AuthzRuleM{allow("a-wildcard", "*"), allow("b-onex", "onex"), deny("block-onex-workspace", "onex")},
			want:  authz.DecisionDeny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAuthorizer(t, tt.rules)
			decision, reason, err := a.Authorize(context.Background(), req)
			if err != nil {
				t.Fatalf("Authorize: %v — an eval error is read as NoOpinion, "+
					"so every rule here is silently void against the fail-closed default", err)
			}
			if decision != tt.want {
				t.Fatalf("got %v, want %v (reason %q)", decision, tt.want, reason)
			}
			if reason == "" {
				t.Error("the decision must name the rule(s) it came from")
			}
		})
	}
}

// TestAgentScopeAppliesToEveryAgentInAMergedRun pins the multi-Agent rule: a
// tool is reachable only through some selected Agent, so a rule scoped to one of
// them must decide the call. The bug this prevents is a merged run presenting
// "a.b" as its agent name, which matches no single-Agent scope — an agent-scoped
// deny then stops applying exactly when a second Agent is added.
func TestAgentScopeAppliesToEveryAgentInAMergedRun(t *testing.T) {
	tests := []struct {
		name       string
		agentNames []string
		want       authz.Decision
	}{
		{name: "single Agent in scope", agentNames: []string{"a"}, want: authz.DecisionDeny},
		{name: "merged run, in-scope Agent selected", agentNames: []string{"a", "b"}, want: authz.DecisionDeny},
		{name: "merged run, in-scope Agent selected second", agentNames: []string{"b", "a"}, want: authz.DecisionDeny},
		{name: "merged run, only out-of-scope Agents", agentNames: []string{"b", "c"}, want: authz.DecisionNoOpinion},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAuthorizer(t, []model.AuthzRuleM{{
				Name: "deny-tool-for-a", SubjectType: "*", SubjectID: []string{"*"},
				ResourceKind: "tool", ResourceName: []string{"dangerous"},
				AgentNames: []string{"a"},
				Verbs:      []string{"read"}, Effect: "deny", Status: enabled,
			}})

			decision, reason, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
				Subject:    authz.Subject{Kind: authz.SubjectKindUser, UserID: "u1"},
				Verb:       authz.VerbRead,
				AgentNames: tt.agentNames,
				ToolName:   "dangerous",
			})
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if decision != tt.want {
				t.Fatalf("got %v, want %v (reason %q)", decision, tt.want, reason)
			}
		})
	}
}

func TestAuthorizerLetsAUserInheritAConsoleUserGroupGrant(t *testing.T) {
	// group_codes is the console-managed membership channel. It must work
	// through the same policy branch as the legacy on-call codes.
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "rule-usergroup", SubjectType: "group", SubjectID: []string{"dept-ai"},
			ResourceKind: "agent", ResourceName: []string{"researcher"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject: authz.Subject{
			Kind: authz.SubjectKindUser, UserID: "uuid-alice",
			Extra: map[string]string{"group_codes": "dept-ai"},
		},
		Verb:      authz.VerbRead,
		AgentName: "researcher",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v, want Allow: a member of dept-ai should inherit the group's agent grant", decision)
	}
}

func TestAuthorizerGroupDenyOverridesUserAllow(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "user-allow", SubjectType: "user", SubjectID: []string{"uuid-alice"},
			ResourceKind: "agent", ResourceName: []string{"researcher"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
		{
			Name: "group-deny", SubjectType: "group", SubjectID: []string{"dept-ai"},
			ResourceKind: "agent", ResourceName: []string{"researcher"},
			Verbs: []string{"read"}, Effect: "deny", Status: enabled,
		},
	})

	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject: authz.Subject{
			Kind: authz.SubjectKindUser, UserID: "uuid-alice",
			Extra: map[string]string{"group_codes": "dept-ai"},
		},
		Verb:      authz.VerbRead,
		AgentName: "researcher",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != authz.DecisionDeny {
		t.Fatalf("got %v, want Deny: deny overrides the user's allow", decision)
	}
}
