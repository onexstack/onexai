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

// TestCanonicalResourceRefMatchesBareRule is the regression test for the 403
// that rejected every run of an Agent whose controller-resolved references
// were canonical:
//
//	agent "agent" access denied for subject ...:
//	mcp:helper-public/onexai: authorization: all levels NoOpinion (fail-closed)
//
// The rule set is the seed's shape (a bare "onexai" grant); the request is what
// AuthorizeResourceRefs actually sends after the namespace refactor. Before the
// bare-name branch this was NoOpinion->Deny, which is the fail-closed answer.
func TestCanonicalResourceRefMatchesBareRule(t *testing.T) {
	a := newTestAuthorizer(t, []model.AuthzRuleM{
		{
			Name: "allow-namespace", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "namespace", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
		{
			Name: "allow-onexai", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "mcp", ResourceName: []string{"onexai"},
			Verbs: []string{"read"}, Effect: "allow", Status: enabled,
		},
	})

	coord := authz.NewCoordinator(a, a, true).WithNamespaceAuthz(
		func(kind authz.ResourceKind, name string) (string, bool) {
			if kind == authz.ResourceKindMCP && name == "helper-public/onexai" {
				return "helper-public", true
			}
			return "", false
		})

	subj := authz.Subject{Kind: authz.SubjectKindUser, UserID: "go.kong"}
	decision, reason, err := coord.AuthorizeResourceRefs(
		context.Background(), subj, "agent", authz.ResourceKindMCP, []string{"helper-public/onexai"})
	if err != nil {
		t.Fatalf("AuthorizeResourceRefs: %v", err)
	}
	if decision != authz.DecisionAllow {
		t.Fatalf("got %v (%s), want Allow: a bare rule must match the canonical reference", decision, reason)
	}
}

// TestBareResourceRuleMatchesCanonicalRef pins the matching semantics directly
// on the authorizer, for every kind that can carry a namespace qualifier.
func TestBareResourceRuleMatchesCanonicalRef(t *testing.T) {
	const subjectID = "go.kong"
	subj := authz.Subject{Kind: authz.SubjectKindUser, UserID: subjectID}

	tests := []struct {
		name       string
		ruleName   string
		effect     string
		kind       authz.ResourceKind
		requestRef string
		want       authz.Decision
	}{
		{
			name:     "mcp bare rule matches canonical ref",
			ruleName: "onexai", effect: "allow", kind: authz.ResourceKindMCP,
			requestRef: "helper-public/onexai", want: authz.DecisionAllow,
		},
		{
			name:     "skill bare rule matches canonical ref",
			ruleName: "sre-skills", effect: "allow", kind: authz.ResourceKindSkill,
			requestRef: "helper-public/sre-skills", want: authz.DecisionAllow,
		},
		{
			name:     "knowledgebase bare rule matches canonical ref",
			ruleName: "runbooks", effect: "allow", kind: authz.ResourceKindKnowledgeBase,
			requestRef: "helper-public/runbooks", want: authz.DecisionAllow,
		},
		{
			name:     "bare rule is namespace-agnostic",
			ruleName: "onexai", effect: "allow", kind: authz.ResourceKindMCP,
			requestRef: "other-team/onexai", want: authz.DecisionAllow,
		},
		{
			name:     "canonical rule does not match another namespace",
			ruleName: "helper-public/onexai", effect: "allow", kind: authz.ResourceKindMCP,
			requestRef: "other-team/onexai", want: authz.DecisionNoOpinion,
		},
		{
			name:     "canonical rule does not match a bare request",
			ruleName: "helper-public/onexai", effect: "allow", kind: authz.ResourceKindMCP,
			requestRef: "onexai", want: authz.DecisionNoOpinion,
		},
		{
			name:     "canonical rule matches the same canonical ref",
			ruleName: "helper-public/onexai", effect: "allow", kind: authz.ResourceKindMCP,
			requestRef: "helper-public/onexai", want: authz.DecisionAllow,
		},
		{
			name:     "bare deny is namespace-agnostic too",
			ruleName: "onexai", effect: "deny", kind: authz.ResourceKindMCP,
			requestRef: "other-team/onexai", want: authz.DecisionDeny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAuthorizer(t, []model.AuthzRuleM{
				{
					Name: "rule", SubjectType: "user", SubjectID: []string{subjectID},
					ResourceKind: string(tt.kind), ResourceName: []string{tt.ruleName},
					Verbs: []string{"read"}, Effect: tt.effect, Status: enabled,
				},
			})
			decision, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
				Subject: subj, Verb: authz.VerbRead,
				Resource: authz.Resource{Kind: tt.kind, Name: tt.requestRef},
			})
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if decision != tt.want {
				t.Fatalf("got %v, want %v", decision, tt.want)
			}
		})
	}
}
