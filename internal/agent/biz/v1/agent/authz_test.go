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
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/approval"
	"github.com/onexstack/onexai/internal/agent/pkg/checkpoint"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/authz/opa"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
)

// toolCtx is the minimal middleware context needsApproval reads a tool name from.
func toolCtx(name string) *adk.ToolContext { return &adk.ToolContext{Name: name} }

// staticAuthorizer returns an Authorizer that always answers a fixed decision.
func staticAuthorizer(_ string, decision authz.Decision, reason string) authz.Authorizer {
	return authz.AuthorizerFunc(func(context.Context, authz.AuthorizationRequest) (authz.Decision, string, error) {
		return decision, reason, nil
	})
}

func testCoordinator(toolAuthz, resourceAuthz authz.Authorizer, enabled bool) *authz.Coordinator {
	return authz.NewCoordinator(toolAuthz, resourceAuthz, enabled)
}

// authzEngine builds the eino engine over a memory checkpoint store with the
// given authorization coordinator attached. It mirrors approvalEngine, minus the
// static approval policy — these tests exercise authorization, not
// require_approval.
func authzEngine(t *testing.T, url string, coordinator *authz.Coordinator, tools ...tool.BaseTool) agentapi.Engine {
	t.Helper()
	eng := NewEngine(EngineConfig{
		BaseURL:     url,
		Model:       "test-model",
		Tools:       func([]string, []string) []tool.BaseTool { return tools },
		Checkpoints: checkpoint.New(nil, "onex", time.Minute),
		Reserved:    []string{"ask_user_question"},
		Authz:       coordinator,
	})
	if eng == nil {
		t.Fatal("NewEngine returned nil for a configured backend")
	}
	return eng
}

// userCtx attaches a caller identity to a run context, the way AgentBiz.Ask
// does before the engine sees it.
func userCtx() context.Context {
	return authz.ContextWithSubject(context.Background(),
		authz.SubjectFromUser("u_alice", "Alice"))
}

// ungatedRun is a run whose static approval policy gates nothing: everything
// that stops it must come from authorization.
func ungatedRun(runID string) agentapi.RunOptions {
	return agentapi.RunOptions{RunID: runID}
}

func TestApprovalHandlerHoldsACallAuthorizationDoesNotAllow(t *testing.T) {
	const reason = "no rule grants delete_cluster to u_alice"

	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
		streamReply(`{"content":"已删除"}`),
	)
	coord := testCoordinator(
		staticAuthorizer("tool", authz.DecisionDeny, reason),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)
	eng := authzEngine(t, up.URL, coord, newGatedTool(t, spy))

	ch, err := eng.Run(userCtx(), approvalMsgs(), ungatedRun("authz-hold"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drainToPause(t, ch)

	if len(got.requests) != 1 {
		t.Fatalf("the pause names %d requests, want 1: %+v", len(got.requests), got.requests)
	}
	req := got.requests[0]
	if req.ToolName != gatedToolName || req.Kind != agentapi.InterruptKindPermission {
		t.Errorf("the pause is for %q (kind %q), want a permission prompt for %q", req.ToolName, req.Kind, gatedToolName)
	}
	// The card must say why the call was held: a prompt that hides the reason
	// asks a human to rubber-stamp a decision they cannot evaluate.
	if !strings.Contains(req.Question, reason) {
		t.Errorf("the prompt %q does not carry the authorization reason %q", req.Question, reason)
	}
	if calls := spy.calls(); len(calls) != 0 {
		t.Fatalf("the tool ran before the elevation was approved: %v", calls)
	}
}

func TestApprovalHandlerRunsTheHoldOnceAHumanElevates(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
		streamReply(`{"content":"已删除"}`),
	)
	coord := testCoordinator(
		staticAuthorizer("tool", authz.DecisionDeny, "denied"),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)
	eng := authzEngine(t, up.URL, coord, newGatedTool(t, spy))
	runs := ungatedRun("authz-elevate")

	ch, err := eng.Run(userCtx(), approvalMsgs(), runs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drainToPause(t, ch)

	resumed, err := eng.(agentapi.Resumable).Resume(userCtx(), agentapi.ResumeOptions{
		Run:       runs,
		RequestID: got.interrupt.RequestID,
		Decision:  agentapi.DecisionAllow,
		Pending: []agentapi.PendingAddress{
			{Address: got.requests[0].Address, Kind: got.requests[0].Kind, ToolName: got.requests[0].ToolName},
		},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if answer := drainToAnswer(t, resumed); answer != "已删除" {
		t.Errorf("the resumed run answered %q, want %q", answer, "已删除")
	}
	if calls := spy.calls(); len(calls) != 1 || calls[0] != "test" {
		t.Errorf("the tool ran with %v, want exactly [test] — an approval is the elevation that runs it", calls)
	}
}

func TestApprovalHandlerRefusesWhenTheHumanDeclinesTheElevation(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
		streamReply(`{"content":"已取消"}`),
	)
	coord := testCoordinator(
		staticAuthorizer("tool", authz.DecisionDeny, "denied"),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)
	eng := authzEngine(t, up.URL, coord, newGatedTool(t, spy))
	runs := ungatedRun("authz-decline")

	ch, err := eng.Run(userCtx(), approvalMsgs(), runs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drainToPause(t, ch)

	resumed, err := eng.(agentapi.Resumable).Resume(userCtx(), agentapi.ResumeOptions{
		Run:       runs,
		RequestID: got.interrupt.RequestID,
		Decision:  agentapi.DecisionDeny,
		Pending: []agentapi.PendingAddress{
			{Address: got.requests[0].Address, Kind: got.requests[0].Kind, ToolName: got.requests[0].ToolName},
		},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if answer := drainToAnswer(t, resumed); answer != "已取消" {
		t.Errorf("the resumed run answered %q, want %q", answer, "已取消")
	}
	if calls := spy.calls(); len(calls) != 0 {
		t.Errorf("the tool ran despite the refusal: %v", calls)
	}
}

func TestApprovalHandlerRunsAnAllowedCallWithoutAsking(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
		streamReply(`{"content":"已删除"}`),
	)
	coord := testCoordinator(
		staticAuthorizer("tool", authz.DecisionAllow, "granted"),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)
	eng := authzEngine(t, up.URL, coord, newGatedTool(t, spy))

	ch, err := eng.Run(userCtx(), approvalMsgs(), ungatedRun("authz-allow"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if answer := drainToAnswer(t, ch); answer != "已删除" {
		t.Errorf("the run answered %q, want %q", answer, "已删除")
	}
	if calls := spy.calls(); len(calls) != 1 {
		t.Errorf("the allowed tool ran %d times, want 1 — an allowed call must not be held", len(calls))
	}
}

func TestApprovalHandlerHoldsWhenAuthorizationCannotAnswer(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
	)
	errAuthz := authz.AuthorizerFunc(func(context.Context, authz.AuthorizationRequest) (authz.Decision, string, error) {
		return authz.DecisionNoOpinion, "", errors.New("policy engine unreachable")
	})
	eng := authzEngine(t, up.URL, testCoordinator(errAuthz, staticAuthorizer("resource", authz.DecisionAllow, ""), true),
		newGatedTool(t, spy))

	ch, err := eng.Run(userCtx(), approvalMsgs(), ungatedRun("authz-error"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drainToPause(t, ch)

	if len(got.requests) != 1 {
		t.Fatalf("an unreachable authorization engine must hold the call, got %d requests", len(got.requests))
	}
	if !strings.Contains(got.requests[0].Question, "权限校验失败") {
		t.Errorf("the prompt %q does not say the check failed", got.requests[0].Question)
	}
	if calls := spy.calls(); len(calls) != 0 {
		t.Errorf("the tool ran although authorization could not answer: %v", calls)
	}
}

// TestApprovalHandlerSkipsAuthorizationWhenNothingEnablesIt pins the
// no-regression contract: with a nil gate the handler is byte-for-byte the
// policy-only one.
func TestApprovalHandlerSkipsAuthorizationWhenNothingEnablesIt(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"test"}`)),
		streamReply(`{"content":"已删除"}`),
	)
	// No Authz at all on the engine config.
	eng := approvalEngine(t, up.URL, newGatedTool(t, spy))

	ch, err := eng.Run(userCtx(), approvalMsgs(), ungatedRun("authz-off"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if answer := drainToAnswer(t, ch); answer != "已删除" {
		t.Errorf("the run answered %q, want %q", answer, "已删除")
	}
	if calls := spy.calls(); len(calls) != 1 {
		t.Errorf("without authz an ungated tool runs directly, got %d calls", len(calls))
	}
}

// storedRules is a RuleSource over rows held in memory — the shape the MySQL
// loader returns.
type storedRules struct {
	rules []*model.AuthzRuleM
	err   error
}

func (s storedRules) List(context.Context, *where.Options) (int64, []*model.AuthzRuleM, error) {
	return int64(len(s.rules)), s.rules, s.err
}

// TestAStoredRuleRowBecomesAQuestionForAHuman is the whole subsystem in one
// test: a row of the shape the database holds, through the real OPA rule store
// and Rego policy, into the engine's gate, out as an approval interrupt on the
// run's stream.
//
// Every other test here substitutes a hand-built Coordinator or a hand-built
// rule snapshot, so each seam is covered only in isolation. This one crosses all
// of them, which is the only way a mistake in the wiring between them — a
// renamed field on the way into the policy, a gate built without the run's
// tools — shows up as the failure it is rather than as its neighbours staying
// green.
func TestAStoredRuleRowBecomesAQuestionForAHuman(t *testing.T) {
	const ruleName = "deny-cluster-deletion"

	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"prod"}`)),
		streamReply(`{"content":"已取消"}`),
	)

	ruleStore := opa.NewRuleStore(storedRules{rules: []*model.AuthzRuleM{{
		Name: ruleName, SubjectType: "*", SubjectID: []string{"*"},
		ResourceKind: "tool", ResourceName: []string{gatedToolName},
		Verbs: []string{"read"}, Effect: "deny", Status: model.StatusEnabled,
	}}})
	if _, err := ruleStore.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	policy, err := opa.NewAuthorizer(context.Background(), ruleStore)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	coord := authz.NewCoordinator(policy, policy, true)
	eng := authzEngine(t, up.URL, coord, newGatedTool(t, spy))
	runs := ungatedRun("stored-rule")

	ch, err := eng.Run(userCtx(), approvalMsgs(), runs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drainToPause(t, ch)

	if len(got.requests) != 1 {
		t.Fatalf("a deny rule produced %d pending requests, want 1", len(got.requests))
	}
	// The card names the rule that held the call: that is what a human reads to
	// decide whether they are overriding the right thing.
	if !strings.Contains(got.requests[0].Question, ruleName) {
		t.Errorf("the prompt %q does not name the rule %q that produced it", got.requests[0].Question, ruleName)
	}
	if calls := spy.calls(); len(calls) != 0 {
		t.Fatalf("the tool ran despite the deny: %v", calls)
	}

	// And the escape hatch works end to end: a human's approval is what runs it.
	resumed, err := eng.(agentapi.Resumable).Resume(userCtx(), agentapi.ResumeOptions{
		Run:       runs,
		RequestID: got.interrupt.RequestID,
		Decision:  agentapi.DecisionAllow,
		Pending: []agentapi.PendingAddress{
			{Address: got.requests[0].Address, Kind: got.requests[0].Kind, ToolName: got.requests[0].ToolName},
		},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if answer := drainToAnswer(t, resumed); answer != "已取消" {
		t.Errorf("the resumed run answered %q", answer)
	}
	if calls := spy.calls(); len(calls) != 1 || calls[0] != "prod" {
		t.Errorf("the tool ran with %v, want exactly [prod]", calls)
	}
}

// TestARuleThatAllowsRunsTheToolWithoutAsking is the same pipeline with the
// effect flipped, so a gate that held everything regardless of the rules would
// not pass both.
func TestARuleThatAllowsRunsTheToolWithoutAsking(t *testing.T) {
	spy := &gateSpy{}
	up := newApprovalUpstream(t,
		streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"dev"}`)),
		streamReply(`{"content":"已删除"}`),
	)

	ruleStore := opa.NewRuleStore(storedRules{rules: []*model.AuthzRuleM{{
		Name: "allow-for-this-user", SubjectType: "user", SubjectID: []string{"u_alice"},
		ResourceKind: "tool", ResourceName: []string{"*"},
		Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled,
	}}})
	if _, err := ruleStore.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	policy, err := opa.NewAuthorizer(context.Background(), ruleStore)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	eng := authzEngine(t, up.URL, authz.NewCoordinator(policy, policy, true), newGatedTool(t, spy))

	ch, err := eng.Run(userCtx(), approvalMsgs(), ungatedRun("stored-allow"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if answer := drainToAnswer(t, ch); answer != "已删除" {
		t.Errorf("the run answered %q, want %q", answer, "已删除")
	}
	if calls := spy.calls(); len(calls) != 1 || calls[0] != "dev" {
		t.Errorf("the allowed tool ran with %v, want exactly [dev]", calls)
	}
}

// ── needsApproval unit coverage ──

func newGateHandler(t *testing.T, coord *authz.Coordinator, reserved []string) *approvalHandler {
	t.Helper()
	gate := newAuthzGate(coord, nil, []string{"some-agent"}, nil, nil, reserved)
	if gate == nil {
		t.Fatal("newAuthzGate returned nil for an enabled coordinator")
	}
	return newApprovalHandler(approval.New(nil, reserved), false, "").withAuthz(gate)
}

func TestNeedsApprovalNeverGatesAReservedTool(t *testing.T) {
	coord := testCoordinator(
		staticAuthorizer("tool", authz.DecisionDeny, "denied"),
		staticAuthorizer("resource", authz.DecisionDeny, "denied"),
		true,
	)
	h := newGateHandler(t, coord, []string{"ask_user_question"})

	ask, _, _ := h.needsApproval(userCtx(), toolCtx("ask_user_question"), `{}`)
	if ask {
		t.Fatal("the tool whose contract is to consult a human must never be gated on consulting a human")
	}

	// A neighbouring tool with the same coordinator is still held.
	if ask, _, _ := h.needsApproval(userCtx(), toolCtx(gatedToolName), `{}`); !ask {
		t.Fatal("a non-reserved tool denied by authz must be held")
	}
}

func TestNeedsApprovalHoldsWhenTheRunCarriesNoIdentity(t *testing.T) {
	coord := testCoordinator(
		staticAuthorizer("tool", authz.DecisionAllow, "granted"),
		staticAuthorizer("resource", authz.DecisionAllow, ""),
		true,
	)
	h := newGateHandler(t, coord, nil)

	ask, reason, source := h.needsApproval(context.Background(), toolCtx(gatedToolName), `{}`)
	if !ask {
		t.Fatal("a run with no identity must fail closed, not run on an unknown caller")
	}
	if source != agentapi.SourceAuthz || reason == "" {
		t.Fatalf("got source=%q reason=%q, want an authz-sourced reason", source, reason)
	}
}

func TestNeedsApprovalSkipsBothGatesUnderAllowAll(t *testing.T) {
	coord := testCoordinator(
		staticAuthorizer("tool", authz.DecisionDeny, "denied"),
		staticAuthorizer("resource", authz.DecisionDeny, "denied"),
		true,
	)
	h := newApprovalHandler(approval.New([]string{gatedToolName}, nil), true, "").withAuthz(
		newAuthzGate(coord, nil, []string{"some-agent"}, nil, nil, nil))

	if ask, _, _ := h.needsApproval(userCtx(), toolCtx(gatedToolName), `{}`); ask {
		t.Fatal("allow-all means a human already took responsibility for the rest of the run")
	}
}

func TestNeedsApprovalReportsThePolicyGateAsOwner(t *testing.T) {
	h := newApprovalHandler(approval.New([]string{gatedToolName}, nil), false, "")

	ask, reason, source := h.needsApproval(userCtx(), toolCtx(gatedToolName), `{}`)
	if !ask || source != agentapi.SourcePolicy || reason != "" {
		t.Fatalf("got ask=%v reason=%q source=%q, want an unexplained policy gate", ask, reason, source)
	}
	if ask, _, _ := h.needsApproval(userCtx(), toolCtx("read_only"), `{}`); ask {
		t.Fatal("a tool the policy does not name must not be held")
	}
}

// TestAnAgentScopedDenySurvivesAMergedRun is the multi-Agent counterpart of
// TestAStoredRuleRowBecomesAQuestionForAHuman, through the same real Rego
// policy. A Tool is reachable only through some selected Agent, so a deny
// scoped to one of them must decide a run that selected it alongside another.
//
// It failed before the request carried the whole selection: the tool gate passed
// the run's Agents joined as "scope-a.scope-b", which matches no single-Agent
// scope, so adding a second Agent silently lifted a deny written for the first.
func TestAnAgentScopedDenySurvivesAMergedRun(t *testing.T) {
	const ruleName = "deny-gated-tool-for-scope-a"
	scoped := func(agentNames []string) (*opa.Authorizer, error) {
		ruleStore := opa.NewRuleStore(storedRules{rules: []*model.AuthzRuleM{
			// The baseline grant, so "the deny did not apply" is observable as a
			// tool that actually ran rather than as a second fail-closed hold.
			{
				Name: "allow-gated-tool", SubjectType: "*", SubjectID: []string{"*"},
				ResourceKind: "tool", ResourceName: []string{gatedToolName},
				Verbs: []string{"read"}, Effect: "allow", Status: model.StatusEnabled,
			},
			{
				Name: ruleName, SubjectType: "*", SubjectID: []string{"*"},
				ResourceKind: "tool", ResourceName: []string{gatedToolName},
				AgentNames: agentNames,
				Verbs:      []string{"read"}, Effect: "deny", Status: model.StatusEnabled,
			},
		}})
		if _, err := ruleStore.Sync(context.Background()); err != nil {
			return nil, err
		}
		return opa.NewAuthorizer(context.Background(), ruleStore)
	}

	tests := []struct {
		name        string
		scope       []string
		runAgents   []string
		wantHeld    bool
		wantToolRan bool
	}{
		{
			name: "in scope, single Agent", scope: []string{"scope-a"}, runAgents: []string{"scope-a"},
			wantHeld: true,
		},
		{
			name: "in scope, merged run", scope: []string{"scope-a"}, runAgents: []string{"scope-a", "scope-b"},
			wantHeld: true,
		},
		{
			name: "out of scope, merged run", scope: []string{"scope-c"}, runAgents: []string{"scope-a", "scope-b"},
			wantHeld:    false,
			wantToolRan: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := scoped(tt.scope)
			if err != nil {
				t.Fatalf("NewAuthorizer: %v", err)
			}
			spy := &gateSpy{}
			up := newApprovalUpstream(t,
				streamReply(toolCallDelta(0, "call_1", gatedToolName, `{"cluster":"prod"}`)),
				streamReply(`{"content":"done"}`),
			)
			eng := authzEngine(t, up.URL, authz.NewCoordinator(policy, policy, true), newGatedTool(t, spy))

			runs := ungatedRun("merged-run")
			runs.Agents = tt.runAgents

			ch, err := eng.Run(userCtx(), approvalMsgs(), runs)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if tt.wantHeld {
				got := drainToPause(t, ch)
				if len(got.requests) != 1 {
					t.Fatalf("an agent-scoped deny produced %d pending requests, want 1", len(got.requests))
				}
				if !strings.Contains(got.requests[0].Question, ruleName) {
					t.Errorf("the prompt %q does not name the rule %q that produced it", got.requests[0].Question, ruleName)
				}
				if calls := spy.calls(); len(calls) != 0 {
					t.Fatalf("the tool ran despite the agent-scoped deny: %v", calls)
				}
				return
			}

			if answer := drainToAnswer(t, ch); answer != "done" {
				t.Errorf("the run answered %q", answer)
			}
			if calls := spy.calls(); tt.wantToolRan && len(calls) != 1 {
				t.Errorf("the tool ran %d times, want 1", len(calls))
			}
		})
	}
}
