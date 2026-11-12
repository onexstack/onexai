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

// fzDefaultRules mirrors the six `allow-fz-default-agent*` rows of
// hack/authz-rules-seed.sql — the grant that makes the built-in fz-default Agent
// usable from oai-chat-apiserver.
//
// It is duplicated here on purpose rather than parsed out of the SQL: this test
// is the only thing that fails when the seed loses a kind, and a parser would
// make the test agree with a broken seed. When the seed gains a row, add it here.
//
// The seed's other rows are not mirrored: `onex-workspace-grant` belongs to OneX
// and `allow-fz-public-*` is the other Agent's grant, covered by
// fzPublicRules below.
func fzDefaultRules() []*model.AuthzRuleM {
	return []*model.AuthzRuleM{
		{Name: "allow-fz-default-agent", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "agent", ResourceName: []string{"fz-default"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
		{Name: "allow-fz-default-agent-mcp-refs", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "mcp", ResourceName: []string{"zke-kubeagent", "zadig-mcp", "onexai", "oai-chat-apiserver"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
		{Name: "allow-fz-default-agent-tools", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "tool", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
		{Name: "allow-fz-default-agent-workspace", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "workspace", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
		{Name: "allow-fz-default-agent-skills", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "skill", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
		{Name: "allow-fz-default-agent-kb-refs", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "knowledgebase", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
	}
}

// fzPublicRules mirrors the five `allow-fz-public-*` rows of
// hack/authz-rules-seed.sql — the Agent a conversation falls back to when the
// bot cannot tell whether the speaker belongs to this tenant.
//
// It carries no mcp row, as the seed does not. Read that as an inventory of the
// file, **not** as the thing keeping the operational MCP servers out of this
// Agent's reach: rules are not agent-scoped, so fzDefaultRules' mcp row answers
// for fz-public too. TestMCPGrantIsNotScopedToTheAgent pins that, and
// hack/authz-rules-seed.sql spells out what is actually holding the line (the
// agent row's empty mcps column).
func fzPublicRules() []*model.AuthzRuleM {
	return []*model.AuthzRuleM{
		{Name: "allow-fz-public-agent", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "agent", ResourceName: []string{"fz-public"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
		{Name: "allow-fz-public-workspace", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "workspace", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
		{Name: "allow-fz-public-tools", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "tool", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
		{Name: "allow-fz-public-skills", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "skill", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
		{Name: "allow-fz-public-kb-refs", SubjectType: "user", SubjectID: []string{"*"},
			ResourceKind: "knowledgebase", ResourceName: []string{"*"},
			Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled},
	}
}

// fzPublicMCPDeny is the seed's one agent-scoped rule, and its only deny.
//
// It exists because the resource-reference check is opt-out: an MCP whose name
// appears in no rule at all is *allowed*, so withholding an allow from fz-public
// takes nothing away — the global allow-fz-default-agent-mcp-refs answers for it
// either way. An explicit, agent-scoped deny is the only construct that says
// "this Agent may not reach this resource".
//
// resource_name is ["*"] rather than the four operational MCP names because of
// that same opt-out default: a name left off the list would be exposed silently.
func fzPublicMCPDeny() *model.AuthzRuleM {
	return &model.AuthzRuleM{
		Name: "deny-fz-public-mcp", SubjectType: "user", SubjectID: []string{"*"},
		ResourceKind: "mcp", ResourceName: []string{"*"},
		AgentNames: []string{"fz-public"},
		Verbs:      []string{"read"}, Effect: "deny", Status: model.StatusEnabled}
}

// baselineRules is both Agents' grants plus the one deny: what a deployment with
// the seed applied actually holds for the two built-in Agents.
func baselineRules() []*model.AuthzRuleM {
	return append(append(fzDefaultRules(), fzPublicRules()...), fzPublicMCPDeny())
}

// TestBaselineRuleSetCoversEveryRunPathDimension walks the seed's rows through
// the real Rego policy and asserts that every dimension the /v1/agent/ask run
// path can ask about — namespace, agent, skill, mcp, knowledgebase, tool — is
// granted to a plain Lark user.
//
// The namespace dimension is the one that bit: AuthorizeAgent checks the Agent's
// namespace *before* the Agent itself, so a seed carrying agent/mcp/tool but no
// namespace row is denied at the first dimension and everything after it is dead
// weight (the Union folds all-NoOpinion into Deny and evaluateLevels stops at the
// first Deny). The request shapes below are copied from the Coordinator's
// AuthorizeAgent / AuthorizeResourceRefs / AuthorizeToolCall, so a kind that stops
// being granted fails here rather than on a user's card.
func TestBaselineRuleSetCoversEveryRunPathDimension(t *testing.T) {
	store := NewRuleStore(&fakeRuleSource{rules: baselineRules()})
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(store.Snapshot()); got != len(baselineRules()) {
		t.Fatalf("the store kept %d of the %d seed rows", got, len(baselineRules()))
	}

	a, err := NewAuthorizer(context.Background(), store)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	// The forwarded end-user identity: a Lark user_id, not an open_id.
	subj := authz.Subject{Kind: authz.SubjectKindUser, UserID: "go.kong"}

	tests := []struct {
		name string
		req  authz.AuthorizationRequest
		want authz.Decision
	}{
		{
			name: "workspace gate (runs before the agent gate; empty workspace normalizes to helper-public)",
			req: authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, AgentName: "fz-default",
				Resource: authz.Resource{Kind: authz.ResourceKindNamespace, Name: model.DefaultNamespace}},
			want: authz.DecisionAllow,
		},
		{
			name: "agent gate",
			req:  authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, AgentName: "fz-default"},
			want: authz.DecisionAllow,
		},
		{
			name: "skill reference",
			req: authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, AgentName: "fz-default",
				Resource: authz.Resource{Kind: authz.ResourceKindSkill, Name: "sre-skills"}},
			want: authz.DecisionAllow,
		},
		{
			name: "mcp reference",
			req: authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, AgentName: "fz-default",
				Resource: authz.Resource{Kind: authz.ResourceKindMCP, Name: "onexai"}},
			want: authz.DecisionAllow,
		},
		{
			// What AuthorizeResourceRefs actually sends once the controller's
			// resolved references are canonicalized. The seed's rule is bare;
			// without the bare-name branch this is a fail-closed Deny, which is
			// the 403 seen on every console debug run.
			name: "mcp reference (canonical)",
			req: authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, AgentName: "fz-default",
				Resource: authz.Resource{Kind: authz.ResourceKindMCP, Name: "helper-public/onexai"}},
			want: authz.DecisionAllow,
		},
		{
			name: "knowledgebase reference",
			req: authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, AgentName: "fz-default",
				Resource: authz.Resource{Kind: authz.ResourceKindKnowledgeBase, Name: "runbooks"}},
			want: authz.DecisionAllow,
		},
		{
			name: "tool call",
			req:  authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, ToolName: "pod", MCPName: "zke-kubeagent"},
			want: authz.DecisionAllow,
		},
		// fz-public — the Agent a conversation falls back to when the speaker
		// cannot be placed in this tenant. Every dimension but mcp, which the
		// seed withholds on purpose; TestPublicAgentIsDeniedMCP pins that half.
		{
			name: "fz-public workspace gate",
			req: authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, AgentName: "fz-public",
				Resource: authz.Resource{Kind: authz.ResourceKindNamespace, Name: model.DefaultNamespace}},
			want: authz.DecisionAllow,
		},
		{
			name: "fz-public agent gate",
			req:  authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, AgentName: "fz-public"},
			want: authz.DecisionAllow,
		},
		{
			name: "fz-public skill reference",
			req: authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, AgentName: "fz-public",
				Resource: authz.Resource{Kind: authz.ResourceKindSkill, Name: "sre-skills"}},
			want: authz.DecisionAllow,
		},
		{
			name: "fz-public knowledgebase reference",
			req: authz.AuthorizationRequest{Subject: subj, Verb: authz.VerbRead, AgentName: "fz-public",
				Resource: authz.Resource{Kind: authz.ResourceKindKnowledgeBase, Name: "courses"}},
			want: authz.DecisionAllow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason, err := a.Authorize(context.Background(), tt.req)
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v: the seed grants no rule for this dimension, which a "+
					"fail-closed Union turns into the 403 on the Lark card", got, tt.want)
			}
			if reason == "" {
				t.Error("an Allow must name the rule that granted it")
			}
		})
	}
}

// TestAgentScopedDenyKeepsMCPOutOfPublic asserts the property the seed's deny
// exists for: the operational MCP servers are reachable from fz-default and not
// from fz-public, even though the allow that grants them is unscoped.
//
// # It runs both directions on purpose
//
// The property is a *difference*, so a one-sided assertion would pass for the
// wrong reason. Denying everything everywhere also makes the fz-public half
// green, and an unscoped allow makes the fz-default half green; only running
// both in one pass distinguishes "the deny is scoped to the right Agent" from
// "grants broke generally".
//
// # The two ways this can regress
//
//   - The deny loses its AgentNames. It then denies the MCP for every Agent
//     including fz-default, and fz-default's tool chain stops working — a
//     symptom visible on the internal card, far from this rule.
//   - The deny is dropped or renamed. fz-public is back to Allow, granted by
//     allow-fz-default-agent-mcp-refs, and nothing else notices: the request
//     succeeds and the operational tool chain runs for whoever the bot could
//     not identify.
func TestAgentScopedDenyKeepsMCPOutOfPublic(t *testing.T) {
	store := NewRuleStore(&fakeRuleSource{rules: baselineRules()})
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	a, err := NewAuthorizer(context.Background(), store)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	subj := authz.Subject{Kind: authz.SubjectKindUser, UserID: "go.kong"}

	tests := []struct {
		agentName string
		mcp       string
		want      authz.Decision
	}{
		{agentName: "fz-default", mcp: "zke-kubeagent", want: authz.DecisionAllow},
		{agentName: "fz-default", mcp: "zadig-mcp", want: authz.DecisionAllow},
		{agentName: "fz-public", mcp: "zke-kubeagent", want: authz.DecisionDeny},
		// The canonical spelling the runtime actually uses must not slip past
		// the deny either; the deny branch goes through the same bare-name match.
		{agentName: "fz-public", mcp: "helper-public/zke-kubeagent", want: authz.DecisionDeny},
		// A name no rule mentions: the opt-out default would allow it, and the
		// deny's wildcard is what stops that from being the answer.
		{agentName: "fz-public", mcp: "some-future-ops-mcp", want: authz.DecisionDeny},
	}

	for _, tt := range tests {
		got, reason, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
			Subject: subj, Verb: authz.VerbRead, AgentName: tt.agentName,
			Resource: authz.Resource{Kind: authz.ResourceKindMCP, Name: tt.mcp},
		})
		if err != nil {
			t.Fatalf("Authorize(%s, %s): %v", tt.agentName, tt.mcp, err)
		}
		if got != tt.want {
			t.Fatalf("Authorize(%s, %s) = %v, want %v (reason %q)",
				tt.agentName, tt.mcp, got, tt.want, reason)
		}
	}
}

// TestAgentScopedDenyDoesNotFollowTheAgentOutOfScope pins the other half of the
// dimension: a rule that names no Agent is not agent-scoped, and answers for
// every Agent — which is what every row written before the column existed
// means.
//
// Without this, an empty AgentNames could quietly become "matches nothing", and
// the whole baseline would stop granting anything: the seed's eleven allow rows
// carry no Agent scope, so they would all stop matching and every Lark card
// would 403.
func TestAgentScopedDenyDoesNotFollowTheAgentOutOfScope(t *testing.T) {
	store := NewRuleStore(&fakeRuleSource{rules: baselineRules()})
	if _, err := store.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	a, err := NewAuthorizer(context.Background(), store)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	subj := authz.Subject{Kind: authz.SubjectKindUser, UserID: "go.kong"}

	// An Agent the seed says nothing about, running something the seed also says
	// nothing about — granted only by the unscoped tool wildcard.
	for _, agentName := range []string{"", "some-third-party-agent"} {
		got, _, err := a.Authorize(context.Background(), authz.AuthorizationRequest{
			Subject: subj, Verb: authz.VerbRead, AgentName: agentName,
			ToolName: "pod", MCPName: "zke-kubeagent",
		})
		if err != nil {
			t.Fatalf("Authorize(%q): %v", agentName, err)
		}
		if got != authz.DecisionAllow {
			t.Fatalf("Agent %q was denied the unscoped tool grant (decision %v): an "+
				"empty AgentNames must mean \"not agent-scoped\", not \"matches nothing\"",
				agentName, got)
		}
	}
}
