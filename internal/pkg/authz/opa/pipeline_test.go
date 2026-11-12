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
	"reflect"
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/authz"
)

// TestRuleRowsBecomeDecisions is the test the whole subsystem hinges on: it
// walks a rule row of the shape the database actually stores all the way to a
// Decision, through the same RuleStore.Sync -> rulesToRegoData -> Rego path the
// server uses.
//
// It exists because every other test in this package starts from a hand-built
// snapshot, which means a mistake in the wiring between the stored shape and the
// policy's expectations — the package name in the Rego module, the data document
// path the query names, a field renamed on one side of rulesToRegoData — would
// leave every one of them green while the deployed system answered NoOpinion to
// everything. NoOpinion is fail-closed, so that failure mode presents as "every
// tool call now asks a human", which is a hard thing to trace back to a typo.
func TestRuleRowsBecomeDecisions(t *testing.T) {
	// A row as the MySQL loader hands it over: list columns already decoded,
	// status as stored.
	rows := []*model.AuthzRuleM{
		{
			Name: "allow-ops-tools", SubjectType: "user", SubjectID: []string{"u_alice"},
			ResourceKind: "tool", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled,
		},
		{
			Name: "deny-destructive", SubjectType: "*", SubjectID: []string{"*"},
			ResourceKind: "tool", ResourceName: []string{"delete_cluster"},
			Verbs: []string{"read"}, Effect: "deny", Status: model.StatusEnabled,
		},
	}

	store := NewRuleStore(&fakeRuleSource{rules: rows})
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// The count is asserted because a Sync that silently stored nothing would
	// make the decisions below NoOpinion, which is also a "correct-looking"
	// answer for a rule set that is meant to deny.
	if got := len(store.Snapshot()); got != 2 {
		t.Fatalf("the store kept %d of the 2 rows", got)
	}

	a, err := NewAuthorizer(context.Background(), store)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	alice := authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"}
	stranger := authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_bob"}

	tests := []struct {
		name     string
		subj     authz.Subject
		tool     string
		want     authz.Decision
		wantNote string
	}{
		{
			// The allow rule reaches the policy: if the Rego namespace or the data
			// path were wrong, this would be NoOpinion instead.
			name: "the stored allow rule grants the named user",
			subj: alice, tool: "read_logs", want: authz.DecisionAllow,
			wantNote: "an allow rule that does not reach the policy looks exactly like a policy that denies",
		},
		{
			name: "an unmatched subject is NoOpinion",
			subj: stranger, tool: "read_logs", want: authz.DecisionNoOpinion,
		},
		{
			// Deny wins over the wildcard allow above, for every subject.
			name: "the stored deny rule holds even the allowed user",
			subj: alice, tool: "delete_cluster", want: authz.DecisionDeny,
			wantNote: "the deny must be evaluated, not merely stored",
		},
		{
			name: "the stored deny rule holds a stranger too",
			subj: stranger, tool: "delete_cluster", want: authz.DecisionDeny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
				Subject: tt.subj, Verb: authz.VerbRead,
				AgentName: "some-agent", ToolName: tt.tool, MCPName: "some-mcp",
			})
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v (%s)", got, tt.want, tt.wantNote)
			}
			// A decision the policy reached names the rule that reached it, which
			// is what an operator reads on the approval card to understand why a
			// call was held.
			if got != authz.DecisionNoOpinion && reason == "" {
				t.Errorf("a %v carries no reason naming the rule that produced it", got)
			}
		})
	}
}

// TestDisabledRowsNeverReachThePolicy pins the other half of the pipeline: a row
// an operator switched off must not grant anything, however well it matches.
func TestDisabledRowsNeverReachThePolicy(t *testing.T) {
	store := NewRuleStore(&fakeRuleSource{rules: []*model.AuthzRuleM{{
		Name: "switched-off", SubjectType: "*", SubjectID: []string{"*"},
		ResourceKind: "tool", ResourceName: []string{"*"},
		Verbs: []string{"read"}, Effect: "allow", Status: model.StatusDisabled,
	}}})
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := store.Snapshot(); !reflect.DeepEqual(got, []model.AuthzRuleM{}) && len(got) != 0 {
		t.Fatalf("a disabled row survived the sync: %+v", got)
	}

	a, err := NewAuthorizer(context.Background(), store)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
		Subject: authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
		Verb:    authz.VerbRead, ToolName: "anything",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if decision != authz.DecisionNoOpinion {
		t.Fatalf("got %v from a disabled rule, want NoOpinion", decision)
	}
}

// TestResourceAttributesSurviveTheStoredShape exercises the one field that is
// not a plain list: resource_attributes is stored as a JSON object in a text
// column and parsed on the way into the policy, so a break in that parsing
// would silently turn every attribute-scoped rule into an unscoped one — a
// wider grant than the operator wrote.
func TestResourceAttributesSurviveTheStoredShape(t *testing.T) {
	store := NewRuleStore(&fakeRuleSource{rules: []*model.AuthzRuleM{{
		Name: "only-prod", SubjectType: "*", SubjectID: []string{"*"},
		ResourceKind: "*", ResourceName: []string{"*"},
		ResourceAttributes: `{"env":"prod"}`,
		Verbs:              []string{"read"}, Effect: "allow", Status: model.StatusEnabled,
	}}})
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	a, err := NewAuthorizer(context.Background(), store)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	check := func(env string, want authz.Decision) {
		t.Helper()
		decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
			Subject:  authz.Subject{Kind: authz.SubjectKindUser, UserID: "u_alice"},
			Verb:     authz.VerbRead,
			Resource: authz.Resource{Kind: authz.ResourceKindK8sNamespace, Name: "some-ns", Attributes: map[string]string{"env": env}},
		})
		if err != nil {
			t.Fatalf("Authorize(env=%s): %v", env, err)
		}
		if decision != want {
			t.Fatalf("env=%s: got %v, want %v", env, decision, want)
		}
	}

	check("prod", authz.DecisionAllow)
	check("test", authz.DecisionNoOpinion)
}
