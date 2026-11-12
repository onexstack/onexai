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

// Package casdoor implements authz.Authorizer against a company-internal
// Casdoor (https://casdoor.org) authorization center, via the official
// github.com/casdoor/casdoor-go-sdk Go SDK. It participates at the Agent
// level (Verb=read), Tool level (Verb=read), and Resource level
// (Verb=read/write) — the same Authorizer instance can be wired into
// Coordinator's per-level Authorizer slots; Casdoor's own Casbin policy on the
// server side decides which (sub, obj, act) triples it actually grants.
//
// Field mapping to the SDK's Casbin (sub, obj, act) triple:
//
//	sub = "<Organization>/<Subject.PrimaryID()>"   (see Organization doc below)
//	obj = req.Resource.String() if non-empty, else "<agent|tool>:<name>"
//	act = string(req.Verb)                          (read|write)
//
// This mirrors authz.Resource.String()'s "kind:name" addressing scheme used
// throughout the authz package, so a Casdoor-side policy author sees the exact
// same object identifiers as the ones opa.Authorizer's Rego policy and
// cmdb.Authorizer's resource names use.
package casdoor

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/casdoor/casdoor-go-sdk/casdoorsdk"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// Config configures the Casdoor client and the sub/obj/act mapping.
type Config struct {
	Endpoint         string // Casdoor server base URL, e.g. "https://casdoor.example.com".
	ClientID         string
	ClientSecret     string
	Certificate      string // JWT verification certificate/public key (PEM), as required by the SDK's AuthConfig.Certificate.
	OrganizationName string
	ApplicationName  string

	// Organization is prepended to Subject.PrimaryID() to build the Casbin
	// sub, e.g. sub = "built-in/u_alice". Casdoor's default built-in
	// permission model matches g(r.sub, p.sub) against users/roles stored as
	// "owner/name" — a bare user ID will silently fail to match unless the
	// Casdoor-side policy was authored with a custom model that doesn't use
	// the g() role grouping. This is a configuration knob, not a constant,
	// because the platform user_id and Casdoor's own user identifiers are not
	// guaranteed to be the same string — aligning them is an operational
	// decision for whoever administers the Casdoor deployment.
	Organization string

	// PermissionID/ModelID/ResourceID/EnforcerID/Owner are the five
	// enforcer-selection filters accepted by casdoorsdk.Client.Enforce. All may
	// be left empty (matching the SDK's own test usage) to consult every
	// enforcer/policy Casdoor has configured; set EnforcerID (or the others) to
	// scope evaluation to a specific Casdoor Permission/Model if the
	// organization manages multiple unrelated policy sets under one Casdoor
	// deployment.
	PermissionID string
	ModelID      string
	ResourceID   string
	EnforcerID   string
	Owner        string

	// Timeout bounds every HTTP call this Authorizer makes to Casdoor.
	//
	// The underlying SDK does not accept a per-call context.Context or timeout
	// (see casdoorsdk.Enforce's signature) — its HTTP transport is a single
	// package-level variable set via casdoorsdk.SetHttpClient, which
	// NewAuthorizer calls once at construction time. This means Timeout is
	// effectively process-global for every casdoorsdk call in this binary, not
	// just this Authorizer's — acceptable because the process is not expected
	// to run more than one Casdoor Authorizer instance, but worth knowing if
	// that ever changes.
	Timeout time.Duration
}

// Authorizer implements authz.Authorizer by calling casdoorsdk.Client.Enforce.
type Authorizer struct {
	client *casdoorsdk.Client
	cfg    Config
}

// NewAuthorizer builds a Casdoor-backed Authorizer and applies cfg.Timeout as
// the process-wide HTTP timeout for all casdoorsdk calls (see the Timeout field
// doc for why this is global, not per-instance).
func NewAuthorizer(cfg Config) *Authorizer {
	if cfg.Timeout > 0 {
		casdoorsdk.SetHttpClient(&http.Client{Timeout: cfg.Timeout})
	}
	client := casdoorsdk.NewClient(
		cfg.Endpoint, cfg.ClientID, cfg.ClientSecret, cfg.Certificate, cfg.OrganizationName, cfg.ApplicationName,
	)
	return &Authorizer{client: client, cfg: cfg}
}

func (a *Authorizer) Name() string { return "casdoor" }

func (a *Authorizer) Authorize(_ context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
	sub := a.subjectID(req.Subject)
	if sub == "" {
		// No usable subject identifier (e.g. a Subject with an empty
		// UserID/GroupCode/ServiceName) — nothing meaningful to ask Casdoor.
		return authz.DecisionNoOpinion, "casdoor: subject has no usable identifier", nil
	}

	obj := objectID(req)
	act := string(req.Verb)

	// casdoorsdk.Enforce has no context.Context parameter (see the package doc
	// and Config.Timeout) — cancellation/timeout is enforced solely via the
	// process-wide http.Client configured in NewAuthorizer.
	allowed, err := a.client.Enforce(
		a.cfg.PermissionID, a.cfg.ModelID, a.cfg.ResourceID, a.cfg.EnforcerID, a.cfg.Owner,
		casdoorsdk.CasbinRequest{sub, obj, act},
	)
	if err != nil {
		// The SDK returns (false, err) for BOTH network/HTTP failures and
		// malformed server responses — never conflate that false with a
		// business Deny. Propagate err so the caller (Union) treats this as
		// NoOpinion, exactly like every other Authorizer's execution-error
		// handling in this package tree.
		return authz.DecisionNoOpinion, "", fmt.Errorf("casdoor: enforce(sub=%s, obj=%s, act=%s): %w", sub, obj, act, err)
	}

	if allowed {
		return authz.DecisionAllow, fmt.Sprintf("casdoor: allowed (sub=%s, obj=%s, act=%s)", sub, obj, act), nil
	}
	// Casdoor's Enforce returns a plain bool, with no "no matching policy" vs
	// "matched a deny policy" distinction exposed to the caller — a false here
	// could mean either. Reporting NoOpinion (not Deny) keeps this Authorizer
	// consistent with the rest of the package: an Authorizer that cannot
	// distinguish "explicitly denied" from "has no opinion" must not
	// manufacture a Deny, since that would let a misconfigured/empty Casdoor
	// policy silently block everyone once this Authorizer is enabled, with no
	// static-rule (OPA) escape hatch.
	return authz.DecisionNoOpinion, fmt.Sprintf("casdoor: no matching policy (sub=%s, obj=%s, act=%s)", sub, obj, act), nil
}

// subjectID builds the Casbin sub for req.Subject, prefixed with
// Config.Organization (see its doc for why the prefix is required by Casdoor's
// default built-in permission model).
func (a *Authorizer) subjectID(subj authz.Subject) string {
	id := subj.PrimaryID()
	if id == "" {
		return ""
	}
	if a.cfg.Organization == "" {
		return id
	}
	return a.cfg.Organization + "/" + id
}

// objectID builds the Casbin obj for req using the same "kind:name" addressing
// pair every other Authorizer derives via
// AuthorizationRequest.EffectiveResource — a concrete Resource when present
// (resource/namespace-level checks), or synthesized from ToolName/AgentName
// otherwise (Agent-level/Tool-level checks) — mirroring opa/input.go's
// buildRegoInput, so a policy author configuring both OPA and Casdoor against
// the same request sees identical object identifiers.
func objectID(req authz.AuthorizationRequest) string {
	kind, name := req.EffectiveResource()
	if kind == authz.ResourceKindNone {
		return ""
	}
	return string(kind) + ":" + name
}
