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

package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/authz"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// refStore builds a resource store exposing one agent with the given declared
// resources, for the resource-reference half of the gate.
func refStore(m *model.AgentM) store.AIStore {
	return &fakeAIStore{
		AIStore: store.NewEmptyStore(),
		agents:  map[string]*model.AgentM{m.Name: m},
	}
}

func TestAuthorizeRunDeniesAnAgentTheCallerMayNotUse(t *testing.T) {
	coord := testCoordinator(
		staticAuthorizer("tool", authz.DecisionDeny, "no rule grants this agent"),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)
	subj := authz.SubjectFromUser("u_bob", "Bob")

	err := authorizeRun(context.Background(), coord, store.NewEmptyStore(), subj, []string{"dangerous-agent"}, nil)
	var denied *authz.ErrAgentAccessDenied
	if !errors.As(err, &denied) {
		t.Fatalf("expected *ErrAgentAccessDenied, got %v", err)
	}
	if denied.AgentName != "dangerous-agent" {
		t.Fatalf("got AgentName=%q, want dangerous-agent", denied.AgentName)
	}
}

func TestAuthorizeRunAllowsWhenAuthorizationIsDisabledOrNil(t *testing.T) {
	subj := authz.SubjectFromUser("u_alice", "Alice")

	if err := authorizeRun(context.Background(), nil, store.NewEmptyStore(), subj, []string{"any"}, nil); err != nil {
		t.Fatalf("a nil coordinator must allow: %v", err)
	}

	disabled := testCoordinator(
		staticAuthorizer("tool", authz.DecisionDeny, "denied"),
		staticAuthorizer("resource", authz.DecisionDeny, "denied"),
		false,
	)
	if err := authorizeRun(context.Background(), disabled, store.NewEmptyStore(), subj, []string{"any"}, nil); err != nil {
		t.Fatalf("a disabled coordinator must allow: %v", err)
	}
}

func TestAuthorizeRunAllowsResourceRefsThePolicyGrants(t *testing.T) {
	// The authorizer grants everything, so the Agent's declared skill, MCP and
	// knowledge base each pass. The point of the test is that all three are
	// checked and all three can pass — a run is not denied for declaring
	// references (that is only true when the policy says nothing, see
	// pkg/authz's TestAuthorizeResourceRefsFailsClosedOnAllNoOpinion).
	coord := authz.NewCoordinator(
		staticAuthorizer("agentTool", authz.DecisionAllow, "allowed"),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)

	resources := refStore(&model.AgentM{
		Name:           "ops",
		Skills:         []string{"sre-skills"},
		MCPs:           []string{"k8sgpt"},
		KnowledgeBases: []string{"runbooks"},
	})
	subj := authz.SubjectFromUser("u_alice", "Alice")

	if err := authorizeRun(context.Background(), coord, resources, subj, []string{"ops"}, nil); err != nil {
		t.Fatalf("granted references must not block the run: %v", err)
	}
}

// TestAuthorizeRunFailsClosedForUngrantedResourceRefs is the other half: a
// reference no rule mentions refuses the run. It used to be allowed, which made
// a missing seed row look like a working grant.
func TestAuthorizeRunFailsClosedForUngrantedResourceRefs(t *testing.T) {
	coord := authz.NewCoordinator(
		staticAuthorizer("agentTool", authz.DecisionNoOpinion, ""),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)

	resources := refStore(&model.AgentM{Name: "ops", Skills: []string{"sre-skills"}})
	subj := authz.SubjectFromUser("u_alice", "Alice")

	err := authorizeRun(context.Background(), coord, resources, subj, []string{"ops"}, nil)
	var denied *authz.ErrAgentAccessDenied
	if !errors.As(err, &denied) {
		t.Fatalf("got %v, want *ErrAgentAccessDenied for a skill no rule grants", err)
	}
}

func TestAuthorizeRunLetsAnExplicitRefDenyBlockTheRun(t *testing.T) {
	// Denies exactly the "dangerous-skill" resource name; every other name is
	// NoOpinion (and so, by the opt-out rule, allowed).
	perName := authz.AuthorizerFunc(func(_ context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
		if req.Resource.Name == "dangerous-skill" {
			return authz.DecisionDeny, "skill is blocked", nil
		}
		return authz.DecisionAllow, "", nil
	})
	coord := authz.NewCoordinator(perName, staticAuthorizer("resource", authz.DecisionAllow, ""), true)

	resources := refStore(&model.AgentM{Name: "ops", Skills: []string{"safe-skill", "dangerous-skill"}})
	subj := authz.SubjectFromUser("u_alice", "Alice")

	err := authorizeRun(context.Background(), coord, resources, subj, []string{"ops"}, nil)
	var denied *authz.ErrAgentAccessDenied
	if !errors.As(err, &denied) {
		t.Fatalf("expected *ErrAgentAccessDenied from the blocked skill, got %v", err)
	}
}

// TestSubjectForRunKeepsWhatTheCallerSaysItIs pins the kind. A gateway signing
// a request is a service account, and recording it as a person of the same name
// would hand it whatever that person was granted — or, with no rule mentioning
// the name, fail closed for a reason that reads like a policy bug.
func TestSubjectForRunKeepsWhatTheCallerSaysItIs(t *testing.T) {
	cases := []struct {
		name string
		kind string
		want authz.Subject
	}{
		{
			name: "service account",
			kind: agentapi.PrincipalKindService,
			want: authz.Subject{Kind: authz.SubjectKindServiceAccount, ServiceName: "oai-chat"},
		},
		{
			name: "group",
			kind: agentapi.PrincipalKindGroup,
			want: authz.Subject{Kind: authz.SubjectKindGroup, GroupCode: "oai-chat", UserName: "Kong"},
		},
		{
			name: "user",
			kind: agentapi.PrincipalKindUser,
			want: authz.Subject{Kind: authz.SubjectKindUser, UserID: "oai-chat", UserName: "Kong"},
		},
		{
			name: "unspecified keeps the historical user",
			kind: "",
			want: authz.Subject{Kind: authz.SubjectKindUser, UserID: "oai-chat", UserName: "Kong"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, subjectForRun("oai-chat", "Kong", tc.kind))
		})
	}
}

// TestAuthorizeRunLetsAnOwnerUseTheirAgent pins the ownership rule the console
// relies on: a user who created an agent may run it even though the rule table
// says nothing about that agent. Without it, every console-created agent would
// be unusable until an administrator wrote a rule.
func TestAuthorizeRunLetsAnOwnerUseTheirAgent(t *testing.T) {
	coord := authz.NewCoordinator(
		staticAuthorizer("agentTool", authz.DecisionDeny, "no rule grants this agent"),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)
	resources := refStore(&model.AgentM{Name: "mine", Owner: "u_alice"})
	subj := authz.SubjectFromUser("u_alice", "Alice")

	if err := authorizeRun(context.Background(), coord, resources, subj, []string{"mine"}, nil); err != nil {
		t.Fatalf("an agent's owner must be able to run it: %v", err)
	}
}

// TestAuthorizeRunStillChecksResourceRefsForTheOwner is the boundary of the
// ownership rule: owning an agent is not a grant to the skills, MCP servers and
// knowledge bases it declares.
func TestAuthorizeRunStillChecksResourceRefsForTheOwner(t *testing.T) {
	coord := authz.NewCoordinator(
		staticAuthorizer("agentTool", authz.DecisionDeny, "no rule grants this agent"),
		staticAuthorizer("resource", authz.DecisionNoOpinion, ""),
		true,
	)
	resources := refStore(&model.AgentM{
		Name:   "mine",
		Owner:  "u_alice",
		Skills: []string{"sre-skills"},
	})
	subj := authz.SubjectFromUser("u_alice", "Alice")

	err := authorizeRun(context.Background(), coord, resources, subj, []string{"mine"}, nil)
	var denied *authz.ErrAgentAccessDenied
	if !errors.As(err, &denied) {
		// Getting here means the ownership rule swallowed the ref check too.
		t.Fatalf("got %v, want *ErrAgentAccessDenied for an ungranted skill", err)
	}
}

// TestAuthorizeRunStillRefusesANonOwner keeps the ownership rule narrow.
func TestAuthorizeRunStillRefusesANonOwner(t *testing.T) {
	coord := authz.NewCoordinator(
		staticAuthorizer("agentTool", authz.DecisionDeny, "no rule grants this agent"),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)
	resources := refStore(&model.AgentM{Name: "mine", Owner: "u_alice"})
	subj := authz.SubjectFromUser("u_bob", "Bob")

	err := authorizeRun(context.Background(), coord, resources, subj, []string{"mine"}, nil)
	var denied *authz.ErrAgentAccessDenied
	if !errors.As(err, &denied) {
		t.Fatalf("got %v, want *ErrAgentAccessDenied for a non-owner", err)
	}
}
