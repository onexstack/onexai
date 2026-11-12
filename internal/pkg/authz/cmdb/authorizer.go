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

// Package cmdb implements authz.Authorizer for resource-level authorization of
// CMDB-backed resources (business services, EKS clusters, k8s namespaces). It
// delegates the actual permission decision to a CMDB system through a narrow
// permissionChecker interface.
//
// oai-agent-apiserver has no CMDB client today, so the wiring injects the
// NotImplementedChecker placeholder: every check fails with
// ErrPermissionCheckNotImplemented, which the Union treats as NoOpinion. The
// backend ships disabled by default (authz.cmdb.enabled=false) and the whole
// lookup path is otherwise intact, so wiring a real client later is a
// one-line provider change.
//
// Caching is handled orthogonally by authz.CachedAuthorizer — a decorator that
// wraps any Authorizer (including this one) with a uniform ristretto cache.
package cmdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// ErrPermissionCheckNotImplemented is returned by the placeholder checker. It
// is an execution error, not a business Deny, so the Union maps it to
// NoOpinion and other backends (OPA) can still decide.
var ErrPermissionCheckNotImplemented = errors.New("cmdb: CheckServicePermission is not implemented")

// PermissionAction is the read/write action a CMDB permission check asks about.
type PermissionAction string

const (
	PermissionActionRead  PermissionAction = "read"
	PermissionActionWrite PermissionAction = "write"
)

// handledKinds lists the authz.Resource.Kind values this Authorizer opines on.
// Any other kind is NoOpinion, letting the Union proceed to other Authorizers
// (e.g. OPA, Webhook).
var handledKinds = map[authz.ResourceKind]bool{
	authz.ResourceKindService:      true,
	authz.ResourceKindCluster:      true,
	authz.ResourceKindK8sNamespace: true,
}

// permissionChecker is the subset of a CMDB client this Authorizer needs, kept
// narrow so unit tests can fake just the permission call.
type permissionChecker interface {
	CheckServicePermission(ctx context.Context, userID, serviceName string, action PermissionAction) (bool, error)
}

// Authorizer implements authz.Authorizer for CMDB-backed resources.
type Authorizer struct {
	client permissionChecker
}

// NewAuthorizer builds a CMDB-backed resource Authorizer.
func NewAuthorizer(client permissionChecker) *Authorizer {
	return &Authorizer{client: client}
}

// NotImplementedChecker is the placeholder injected when no CMDB backend is
// configured: every check reports ErrPermissionCheckNotImplemented.
type NotImplementedChecker struct{}

func (NotImplementedChecker) CheckServicePermission(context.Context, string, string, PermissionAction) (bool, error) {
	return false, ErrPermissionCheckNotImplemented
}

func (a *Authorizer) Name() string { return "cmdb" }

func (a *Authorizer) Authorize(ctx context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
	if !handledKinds[req.Resource.Kind] {
		return authz.DecisionNoOpinion, "", nil
	}

	if req.Subject.Kind != authz.SubjectKindUser {
		// The CMDB permission model is assumed to be per-user; a duty-group or
		// service Subject degrades to NoOpinion here rather than Deny, so an
		// OPA rule can still grant access.
		return authz.DecisionNoOpinion, "cmdb: group/service-level CMDB permission not supported, deferring", nil
	}

	action := PermissionActionRead
	if req.Verb == authz.VerbWrite {
		action = PermissionActionWrite
	}

	allowed, err := a.client.CheckServicePermission(ctx, req.Subject.UserID, req.Resource.Name, action)
	if err != nil {
		return authz.DecisionNoOpinion, "", fmt.Errorf("cmdb: permission check failed: %w", err)
	}

	if allowed {
		return authz.DecisionAllow, fmt.Sprintf("cmdb: user %s has %s permission on %s", req.Subject.UserID, action, req.Resource.Name), nil
	}
	return authz.DecisionDeny, fmt.Sprintf("cmdb: user %s has no %s permission on %s", req.Subject.UserID, action, req.Resource.Name), nil
}
