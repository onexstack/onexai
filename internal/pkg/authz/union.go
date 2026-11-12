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
	"log/slog"
)

// unionAuthorizer composes multiple Authorizers with K8s-style Union
// semantics (see k8s.io/apiserver/pkg/authorization/union and
// authorizerfactory.NewUnionAuthorizer):
//
//   - Any Authorizer returning Deny short-circuits immediately as Deny
//     ("Deny wins" — later Denies are never reached, but that's fine because
//     a single Deny already ends the request).
//   - An Allow is recorded but evaluation continues, so a later Authorizer's
//     explicit Deny still takes precedence over an earlier Allow.
//   - If every Authorizer returns NoOpinion (or none are configured), the
//     request is Denied (fail-closed default).
//   - An Authorizer's own execution error is treated as NoOpinion (logged as
//     a warning) rather than aborting the whole evaluation — a single failing
//     backend should not panic-fail authorization for every request.
type unionAuthorizer struct {
	authorizers []Authorizer
}

// NewUnionAuthorizer constructs a Deny-wins / Union-Allow combinator over the
// given Authorizers, evaluated in order. When no Authorizers are given it
// returns Deny, not Allow: "no backend was configured" is a deployment bug,
// and answering Allow for it turns a missing rule source into an unrestricted
// service. See ProvideAuthzCoordinator, which refuses to start in that state.
func NewUnionAuthorizer(authorizers ...Authorizer) Authorizer {
	if len(authorizers) == 0 {
		return AuthorizerFunc(func(_ context.Context, _ AuthorizationRequest) (Decision, string, error) {
			return DecisionDeny, "union: no authorizers configured (fail closed)", nil
		})
	}
	return &unionAuthorizer{authorizers: authorizers}
}

func (u *unionAuthorizer) Name() string { return "union" }

func (u *unionAuthorizer) Authorize(ctx context.Context, req AuthorizationRequest) (Decision, string, error) {
	var results []decisionResult

	for _, a := range u.authorizers {
		// The name that ends up in the reason a person reads; see displayName.
		source := displayName(a)
		decision, reason, err := a.Authorize(ctx, req)
		if err != nil {
			slog.WarnContext(ctx, "authz: authorizer execution error, treated as NoOpinion",
				"authorizer", a.Name(), "error", err)
			results = append(results, decisionResult{
				source: source, decision: DecisionNoOpinion, reason: reason,
			})
			continue
		}
		results = append(results, decisionResult{
			source: source, decision: decision, reason: reason,
		})
		// Deny short-circuits immediately -- a later backend Allow
		// cannot override an earlier Deny (deny-wins semantics),
		// so there is no value in continuing to the next Authorizer.
		if decision == DecisionDeny {
			return DecisionDeny, joinReason(source, reason), nil
		}
	}

	// Backend execution errors inside a Union are always treated as NoOpinion
	// (never Deny) so other healthy backends can still grant access.
	return combineResults(results, DecisionDeny,
		func(_ string, _ error) error { return nil })
}

// displayNamer is implemented by an Authorizer whose Name() carries a marker
// that belongs in metrics but not in a sentence a person reads (the only one is
// CachedAuthorizer's "cached:" prefix).
type displayNamer interface {
	DisplayName() string
}

// displayName returns the name to put in a reason string.
func displayName(a Authorizer) string {
	if d, ok := a.(displayNamer); ok {
		return d.DisplayName()
	}
	return a.Name()
}

func joinReason(authorizerName, reason string) string {
	if reason == "" {
		return authorizerName
	}
	return authorizerName + ": " + reason
}
