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

package roleauthz

import (
	"context"
	"errors"
	"testing"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

// groupRoleAuthorizer grants role:<role> to a group subject whose code is
// exactly wantGroup. Every other question is NoOpinion, which is the engine's
// "no rule spoke".
type groupRoleAuthorizer struct {
	wantGroup string
	wantRole  string
}

func (groupRoleAuthorizer) Name() string { return "fake" }

func (f groupRoleAuthorizer) Authorize(_ context.Context, req authz.AuthorizationRequest) (authz.Decision, string, error) {
	if req.Subject.Kind == authz.SubjectKindGroup &&
		req.Subject.GroupCode == f.wantGroup &&
		req.Resource.Kind == authz.ResourceKindRole &&
		req.Resource.Name == f.wantRole {
		return authz.DecisionAllow, "group grant", nil
	}
	return authz.DecisionNoOpinion, "", nil
}

type fakeGroups struct {
	codes map[string][]string
	err   error
}

func (f fakeGroups) UserGroupCodes(_ context.Context, identity string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.codes[identity], nil
}

func TestUserGroupGrantsARole(t *testing.T) {
	coordinator := authz.NewCoordinator(
		groupRoleAuthorizer{wantGroup: "dept-admin", wantRole: "admin"},
		groupRoleAuthorizer{wantGroup: "dept-admin", wantRole: "admin"},
		true,
	)
	resolver := NewWithGroups(nil, coordinator, fakeGroups{
		codes: map[string][]string{"alice": {"dept-admin"}},
	})

	if !resolver.IsAdminUser("alice") {
		t.Fatal("a member of dept-admin should be an administrator")
	}
	if resolver.IsSREUser("alice") {
		t.Fatal("the group grants admin, not sre")
	}
	if resolver.IsAdminUser("bob") {
		t.Fatal("a non-member must not inherit the group grant")
	}
}

func TestGroupLookupFailureWithholdsTheRole(t *testing.T) {
	coordinator := authz.NewCoordinator(
		groupRoleAuthorizer{wantGroup: "dept-admin", wantRole: "admin"},
		groupRoleAuthorizer{wantGroup: "dept-admin", wantRole: "admin"},
		true,
	)
	resolver := NewWithGroups(nil, coordinator, fakeGroups{err: errors.New("agent down")})

	if resolver.IsAdminUser("alice") {
		t.Fatal("an unavailable membership source must not grant a group role")
	}
}
