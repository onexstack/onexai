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

package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

func newTestUserGroupStore(t *testing.T) *Store {
	t.Helper()
	return newTestStandardStore(t, &model.UserGroupM{}, &model.UserGroupMemberM{})
}

func member(userID, username, lark string) model.UserGroupMemberM {
	return model.UserGroupMemberM{UserID: userID, Username: username, LarkUserID: lark}
}

// TestUserGroupLifecycle pins create/get/update/delete and the atomic member
// replacement.
func TestUserGroupLifecycle(t *testing.T) {
	s := newTestUserGroupStore(t)
	ctx := context.Background()
	groups := s.UserGroups()

	if err := groups.Create(ctx, &model.UserGroupM{
		Name:        "dept-ai",
		DisplayName: "AI 组",
		Enabled:     true,
		Owner:       "owner-1",
		Members: []model.UserGroupMemberM{
			member("uuid-alice", "alice", "go.kong"),
			member("uuid-bob", "bob", ""),
		},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := groups.Get(ctx, "dept-ai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "AI 组" || len(got.Members) != 2 {
		t.Fatalf("get = %+v", got)
	}

	// Update replaces the member set: bob leaves, carol joins.
	if err := groups.Update(ctx, &model.UserGroupM{
		Name:        "dept-ai",
		DisplayName: "AI 组（新）",
		Enabled:     true,
		Members: []model.UserGroupMemberM{
			member("uuid-alice", "alice", "go.kong"),
			member("uuid-carol", "carol", "carol.lark"),
		},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err = groups.Get(ctx, "dept-ai")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.DisplayName != "AI 组（新）" || len(got.Members) != 2 {
		t.Fatalf("get after update = %+v", got)
	}
	for _, m := range got.Members {
		if m.UserID == "uuid-bob" {
			t.Fatalf("bob should have been removed: %+v", got.Members)
		}
	}

	if err := groups.Delete(ctx, "dept-ai"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := groups.Get(ctx, "dept-ai"); !errors.Is(err, agentstore.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	// Deleting again converges.
	if err := groups.Delete(ctx, "dept-ai"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

// TestUserGroupCodesMatchEveryAlias is the contract the authorization resolver
// depends on: any identifier the platform may see resolves to the group.
func TestUserGroupCodesMatchEveryAlias(t *testing.T) {
	s := newTestUserGroupStore(t)
	ctx := context.Background()
	groups := s.UserGroups()

	if err := groups.Create(ctx, &model.UserGroupM{
		Name:    "dept-ai",
		Enabled: true,
		Members: []model.UserGroupMemberM{
			member("uuid-alice", "alice", "go.kong"),
		},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := groups.Create(ctx, &model.UserGroupM{
		Name:    "paused",
		Enabled: false,
		Members: []model.UserGroupMemberM{
			member("uuid-alice", "alice", "go.kong"),
		},
	}); err != nil {
		t.Fatalf("create paused: %v", err)
	}

	for _, identity := range []string{"uuid-alice", "alice", "go.kong"} {
		codes, err := groups.GroupCodesFor(ctx, identity)
		if err != nil {
			t.Fatalf("codes for %s: %v", identity, err)
		}
		if len(codes) != 1 || codes[0] != "dept-ai" {
			t.Fatalf("codes for %s = %v, want [dept-ai] (disabled group excluded)", identity, codes)
		}
	}

	if codes, err := groups.GroupCodesFor(ctx, "nobody"); err != nil || len(codes) != 0 {
		t.Fatalf("codes for unknown = %v, %v", codes, err)
	}
}

// TestUserGroupListFilters covers the two filters the console uses.
func TestUserGroupListFilters(t *testing.T) {
	s := newTestUserGroupStore(t)
	ctx := context.Background()
	groups := s.UserGroups()

	for _, name := range []string{"dept-ai", "dept-ops"} {
		if err := groups.Create(ctx, &model.UserGroupM{
			Name:        name,
			DisplayName: name,
			Enabled:     true,
			Members:     []model.UserGroupMemberM{member("uuid-"+name, name, "")},
		}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	total, rows, err := groups.List(ctx, agentstore.ListUserGroupsOptions{Keyword: "OPS"})
	if err != nil {
		t.Fatalf("list keyword: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].Name != "dept-ops" {
		t.Fatalf("keyword list = %d, %+v", total, rows)
	}

	total, rows, err = groups.List(ctx, agentstore.ListUserGroupsOptions{Member: "uuid-dept-ai"})
	if err != nil {
		t.Fatalf("list member: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].Name != "dept-ai" {
		t.Fatalf("member list = %d, %+v", total, rows)
	}

	total, rows, err = groups.List(ctx, agentstore.ListUserGroupsOptions{Offset: 0, Limit: 1})
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	if total != 2 || len(rows) != 1 {
		t.Fatalf("paged list = %d, %d rows", total, len(rows))
	}
}
