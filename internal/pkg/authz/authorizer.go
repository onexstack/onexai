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

import "context"

// Decision is the outcome of an Authorizer.Authorize call, analogous to
// k8s.io/apiserver/pkg/authorization/authorizer.Decision.
type Decision int

const (
	// DecisionNoOpinion means this Authorizer did not opine on the request;
	// the Union combinator should consult the next Authorizer.
	DecisionNoOpinion Decision = iota
	DecisionAllow
	DecisionDeny
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "Allow"
	case DecisionDeny:
		return "Deny"
	default:
		return "NoOpinion"
	}
}

// Authorizer answers whether a given AuthorizationRequest should be allowed.
// Each implementation (OPA, CMDB, Webhook, ...) only needs to answer "do I
// have an opinion on this request" — the Union combinator (see union.go)
// decides the final outcome across multiple Authorizers.
type Authorizer interface {
	// Authorize returns this Authorizer's Decision plus a human-readable
	// reason (used in logs and user-facing denial messages).
	//
	// err represents the Authorizer's own execution failure (network
	// timeout, policy compile error, etc) — NOT a business-level Deny. The
	// returned Decision in the error case should be treated as advisory only;
	// callers must decide how to map err to a final Decision (Union treats it
	// as NoOpinion and logs a warning; see AuthzOptions.FailOpen for the
	// global override).
	Authorize(ctx context.Context, req AuthorizationRequest) (decision Decision, reason string, err error)

	// Name identifies this Authorizer for logging and metrics.
	Name() string
}

// AuthorizerFunc adapts a plain function to the Authorizer interface,
// analogous to k8s authorizer.AuthorizerFunc. Useful for tests and for
// trivial static rules (e.g. "always allow service subjects").
type AuthorizerFunc func(ctx context.Context, req AuthorizationRequest) (Decision, string, error)

func (f AuthorizerFunc) Authorize(ctx context.Context, req AuthorizationRequest) (Decision, string, error) {
	return f(ctx, req)
}

func (f AuthorizerFunc) Name() string { return "func" }
