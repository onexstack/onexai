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

package store

import (
	"context"
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
)

func TestResolveByName(t *testing.T) {
	db := newStoreTestDB(t, &model.UserM{}, &model.UserLarkBindingM{})
	ctx := context.Background()
	users := newUserStore(&store{db: db})

	seed := func(id, username, email, larkID string) {
		t.Helper()
		if err := users.Create(ctx, &model.UserM{
			ID: id, Username: username, Email: email, Status: model.UserStatusActive,
		}); err != nil {
			t.Fatalf("seed %s: %v", username, err)
		}
		if larkID != "" {
			if err := users.PutLarkBinding(ctx, &model.UserLarkBindingM{
				UserID: id, OpenID: "ou-" + id, LarkUserID: larkID,
			}); err != nil {
				t.Fatalf("seed binding %s: %v", username, err)
			}
		}
	}

	seed("u1", "go.kong", "go.kong@example.com", "lark-go")
	seed("u2", "ops.luo", "ops.luo@example.com", "lark-ops")
	// Two accounts share an email: ambiguous and must not resolve to a person.
	seed("u3", "dup.one", "shared@example.com", "")
	seed("u4", "dup.two", "shared@example.com", "")

	tests := []struct {
		name       string
		lookup     string
		wantUser   string
		wantLarkID string
	}{
		{name: "username exact", lookup: "go.kong", wantUser: "u1", wantLarkID: "lark-go"},
		{name: "username case-insensitive", lookup: "GO.KONG", wantUser: "u1", wantLarkID: "lark-go"},
		{name: "email case-insensitive", lookup: "OPS.LUO@EXAMPLE.COM", wantUser: "u2", wantLarkID: "lark-ops"},
		{name: "unique email", lookup: "go.kong@example.com", wantUser: "u1", wantLarkID: "lark-go"},
		{name: "ambiguous email", lookup: "shared@example.com"},
		{name: "unknown", lookup: "nobody@example.com"},
		{name: "blank", lookup: "  "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			user, binding, err := users.ResolveByName(ctx, tc.lookup)
			if err != nil {
				t.Fatalf("ResolveByName(%q) error = %v", tc.lookup, err)
			}
			if tc.wantUser == "" {
				if user != nil || binding != nil {
					t.Fatalf("ResolveByName(%q) = (%+v, %+v), want no resolution", tc.lookup, user, binding)
				}
				return
			}
			if user == nil || user.ID != tc.wantUser {
				t.Fatalf("ResolveByName(%q) user = %+v, want id %s", tc.lookup, user, tc.wantUser)
			}
			if binding == nil || binding.LarkUserID != tc.wantLarkID {
				t.Fatalf("ResolveByName(%q) binding = %+v, want lark id %s", tc.lookup, binding, tc.wantLarkID)
			}
		})
	}
}

func TestResolveByNameWithoutBinding(t *testing.T) {
	db := newStoreTestDB(t, &model.UserM{}, &model.UserLarkBindingM{})
	ctx := context.Background()
	users := newUserStore(&store{db: db})

	if err := users.Create(ctx, &model.UserM{
		ID: "u1", Username: "local.user", Email: "local@example.com", Status: model.UserStatusActive,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	user, binding, err := users.ResolveByName(ctx, "local.user")
	if err != nil {
		t.Fatalf("ResolveByName error = %v", err)
	}
	if user == nil || user.ID != "u1" {
		t.Fatalf("user = %+v, want u1", user)
	}
	if binding != nil {
		t.Fatalf("binding = %+v, want nil for an unbound account", binding)
	}
}
