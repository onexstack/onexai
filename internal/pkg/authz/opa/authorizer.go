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

// Package opa implements authz.Authorizer by evaluating an embedded Rego
// policy (policy/authz.rego) against a rule set stored in the coredb
// agent.authz_rule table (see RuleStore) and the per-request attributes of an
// authz.AuthorizationRequest.
//
// The Go SDK's embedded evaluator (rego.New/PrepareForEval) is used rather
// than a standalone OPA server process: oai-agent-apiserver is a single process with
// no existing policy-distribution infrastructure, and embedding lets the
// "periodic sync -> atomically swap in-memory data" pattern drive policy
// reloads directly, with no extra network hop per Tool call.
package opa

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/open-policy-agent/opa/v1/rego"
	"github.com/open-policy-agent/opa/v1/storage/inmem"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

//go:embed policy/authz.rego
var regoPolicyModule string

// Authorizer implements authz.Authorizer by evaluating the embedded Rego
// policy against a data.authz.rules document built from RuleStore.Snapshot.
// It handles Agent-level (Verb=read), Tool-level (Verb=read), and
// Resource-level requests — the policy dispatches on resource_kind, so one
// Authorizer instance backs both Coordinator.agentToolAuthz and
// Coordinator.resourceAuthz.
type Authorizer struct {
	ruleStore *RuleStore

	// query is replaced as a whole by Reload, which runs on the periodic
	// refresher's goroutine and on every rule write while request goroutines are
	// calling Authorize. An atomic pointer makes the swap visible without a lock
	// on the hot path; a prepared query is immutable once prepared, so readers
	// never observe a partially built one.
	query atomic.Pointer[rego.PreparedEvalQuery]
}

// NewAuthorizer builds the prepared query from the RuleStore's current
// snapshot. Call Reload after every RuleStore.Sync so evaluation sees the
// latest rule data.
func NewAuthorizer(ctx context.Context, ruleStore *RuleStore) (*Authorizer, error) {
	a := &Authorizer{ruleStore: ruleStore}
	if err := a.Reload(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

// Reload re-prepares the query with a fresh data.authz.rules snapshot.
func (a *Authorizer) Reload(ctx context.Context) error {
	store := inmem.NewFromObject(map[string]any{
		"authz": map[string]any{
			"rules": rulesToRegoData(a.ruleStore.Snapshot()),
		},
	})

	prepared, err := rego.New(
		rego.Query("data.oai.authz.decision"),
		rego.Module("authz.rego", regoPolicyModule),
		rego.Store(store),
	).PrepareForEval(ctx)
	if err != nil {
		return fmt.Errorf("opa: prepare query: %w", err)
	}

	a.query.Store(&prepared)
	return nil
}

func (a *Authorizer) Name() string { return "opa" }

func (a *Authorizer) Authorize(ctx context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
	query := a.query.Load()
	if query == nil {
		return authz.DecisionNoOpinion, "", errors.New("opa: authorizer has no prepared query (Reload never succeeded)")
	}
	input := buildRegoInput(req)

	rs, err := query.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return authz.DecisionNoOpinion, "", fmt.Errorf("opa: eval: %w", err)
	}
	return parseRegoResult(rs)
}
