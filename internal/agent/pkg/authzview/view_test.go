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

package authzview

import (
	"context"
	"testing"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// fakeAuthorizer answers from a fixed table keyed by "kind/name".
type fakeAuthorizer struct {
	answers map[string]authz.Decision
}

func (f fakeAuthorizer) Name() string { return "fake" }

func (f fakeAuthorizer) Authorize(_ context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
	key := string(req.Resource.Kind) + "/" + req.Resource.Name
	if decision, ok := f.answers[key]; ok {
		return decision, "fake", nil
	}
	return authz.DecisionNoOpinion, "", nil
}

func newTestState(answers map[string]authz.Decision, read, write Mode, admin bool) *State {
	coordinator := authz.NewCoordinator(fakeAuthorizer{answers: answers}, fakeAuthorizer{answers: answers}, true)
	return NewState(coordinator, authz.SubjectFromUser("uuid-alice", "Alice"), admin, read, write, nil)
}

func TestVisibleDenyWins(t *testing.T) {
	state := newTestState(map[string]authz.Decision{
		"agent/researcher": authz.DecisionDeny,
	}, ModeAdditive, ModeAdditive, false)

	if state.Visible(context.Background(), "agent", "", "researcher", "") {
		t.Fatal("a denied resource must not be visible")
	}
}

func TestVisibleAllowShows(t *testing.T) {
	state := newTestState(map[string]authz.Decision{
		"agent/researcher": authz.DecisionAllow,
	}, ModeAdditive, ModeAdditive, false)

	if !state.Visible(context.Background(), "agent", "", "researcher", "") {
		t.Fatal("an allowed resource must be visible")
	}
}

func TestVisibleNamespaceFallback(t *testing.T) {
	// No rule for the agent itself, but the enclosing namespace is allowed.
	state := newTestState(map[string]authz.Decision{
		"namespace/helper-public": authz.DecisionAllow,
	}, ModeAuthoritative, ModeAuthoritative, false)

	if !state.Visible(context.Background(), "agent", "helper-public", "researcher", "") {
		t.Fatal("a namespace grant must make the resource visible")
	}
}

func TestVisibleNoOpinionDependsOnMode(t *testing.T) {
	answers := map[string]authz.Decision{}

	additive := newTestState(answers, ModeAdditive, ModeAdditive, false)
	if !additive.Visible(context.Background(), "agent", "", "researcher", "") {
		t.Fatal("additive mode keeps the legacy visibility when no rule speaks")
	}

	authoritative := newTestState(answers, ModeAuthoritative, ModeAuthoritative, false)
	if authoritative.Visible(context.Background(), "agent", "", "researcher", "") {
		t.Fatal("authoritative mode hides a row no rule covers")
	}
}

func TestVisibleAdminBypassesDenyButOwnerDoesNot(t *testing.T) {
	answers := map[string]authz.Decision{"agent/researcher": authz.DecisionDeny}

	admin := newTestState(answers, ModeAuthoritative, ModeAuthoritative, true)
	if !admin.Visible(context.Background(), "agent", "", "researcher", "") {
		t.Fatal("an administrator sees every row")
	}

	owner := newTestState(answers, ModeAuthoritative, ModeAuthoritative, false)
	if owner.Visible(context.Background(), "agent", "", "researcher", "uuid-alice") {
		t.Fatal("an explicit Deny must hide the row even from its owner")
	}
	if owner.Visible(context.Background(), "agent", "", "researcher", "someone-else") {
		t.Fatal("a non-owner must not inherit the owner bypass")
	}
}

func TestVisibleOwnerSeesAnUncoveredRow(t *testing.T) {
	state := newTestState(map[string]authz.Decision{}, ModeAuthoritative, ModeAuthoritative, false)
	if !state.Visible(context.Background(), "agent", "", "researcher", "uuid-alice") {
		t.Fatal("an owner sees their own row when no rule speaks")
	}
	if state.Visible(context.Background(), "agent", "", "researcher", "someone-else") {
		t.Fatal("authoritative mode hides an uncovered row from a non-owner")
	}
}

func TestFilterAndAdjustCount(t *testing.T) {
	state := newTestState(map[string]authz.Decision{
		"agent/hidden": authz.DecisionDeny,
	}, ModeAdditive, ModeAdditive, false)
	ctx := WithState(context.Background(), state)

	rows := []struct{ name string }{{"visible"}, {"hidden"}}
	out := Filter(ctx, "agent", rows, func(row struct{ name string }) (string, string, string) {
		return "", row.name, ""
	})
	if len(out) != 1 || out[0].name != "visible" {
		t.Fatalf("filtered rows = %+v", out)
	}
	if got := AdjustCount(10, 3); got != 7 {
		t.Fatalf("adjust = %d, want 7", got)
	}
	if got := AdjustCount(1, 3); got != 0 {
		t.Fatalf("adjust below zero = %d, want 0", got)
	}
}
