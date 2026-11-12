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
	"time"

	"github.com/onexstack/onexai/internal/chat/model"
)

// seedBinding writes one minimal binding, the shape FeishuBindConfirm produced
// before it learned to resolve the contact record.
func seedBinding(t *testing.T, users UserStore, userID, openID string) {
	t.Helper()
	if err := users.PutLarkBinding(context.Background(), &model.UserLarkBindingM{
		UserID: userID, OpenID: openID, BoundVia: "code",
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
}

func TestUpdateLarkBindingProfileFillsOnlyEmptyColumns(t *testing.T) {
	db := newStoreTestDB(t, &model.UserLarkBindingM{})
	ctx := context.Background()
	users := newUserStore(&store{db: db})
	seedBinding(t, users, "u1", "ou-1")

	// Pretend another writer already stored a name and an old updated_at.
	if err := db.Model(&model.UserLarkBindingM{}).
		Where("user_id = ?", "u1").
		Updates(map[string]any{"name": "Keep Me", "updated_at": time.Now().Add(-time.Hour)}).Error; err != nil {
		t.Fatalf("pre-seed name: %v", err)
	}
	before, err := users.LarkBinding(ctx, "u1")
	if err != nil || before == nil {
		t.Fatalf("read binding: %v", err)
	}

	err = users.UpdateLarkBindingProfile(ctx, "u1", "ou-1", model.LarkProfile{
		LarkUserID: "lkong",
		UnionID:    "on-1",
		Name:       "Lingfei Kong",
		AvatarURL:  "https://x/640.png",
	})
	if err != nil {
		t.Fatalf("UpdateLarkBindingProfile() error = %v", err)
	}

	after, err := users.LarkBinding(ctx, "u1")
	if err != nil || after == nil {
		t.Fatalf("read binding after update: %v", err)
	}
	if after.LarkUserID != "lkong" {
		t.Errorf("LarkUserID = %q, want lkong", after.LarkUserID)
	}
	if after.UnionID != "on-1" {
		t.Errorf("UnionID = %q, want on-1", after.UnionID)
	}
	if after.Name != "Keep Me" {
		t.Errorf("Name = %q, want the pre-existing value untouched", after.Name)
	}
	if after.AvatarURL != "https://x/640.png" {
		t.Errorf("AvatarURL = %q, want the filled avatar", after.AvatarURL)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("UpdatedAt moved from %v to %v; a profile repair is not a binding event",
			before.UpdatedAt, after.UpdatedAt)
	}
}

func TestUpdateLarkBindingProfileRequiresMatchingOpenID(t *testing.T) {
	db := newStoreTestDB(t, &model.UserLarkBindingM{})
	ctx := context.Background()
	users := newUserStore(&store{db: db})
	seedBinding(t, users, "u1", "ou-1")

	err := users.UpdateLarkBindingProfile(ctx, "u1", "ou-other", model.LarkProfile{LarkUserID: "lkong"})
	if err != nil {
		t.Fatalf("UpdateLarkBindingProfile() error = %v", err)
	}
	after, err := users.LarkBinding(ctx, "u1")
	if err != nil || after == nil {
		t.Fatalf("read binding: %v", err)
	}
	if after.LarkUserID != "" {
		t.Errorf("LarkUserID = %q, want empty: the open_id did not match", after.LarkUserID)
	}
}

func TestUpdateLarkBindingProfileDoesNotResurrect(t *testing.T) {
	db := newStoreTestDB(t, &model.UserLarkBindingM{})
	ctx := context.Background()
	users := newUserStore(&store{db: db})
	seedBinding(t, users, "u1", "ou-1")

	if err := users.DeleteLarkBinding(ctx, "u1"); err != nil {
		t.Fatalf("DeleteLarkBinding() error = %v", err)
	}
	if err := users.UpdateLarkBindingProfile(ctx, "u1", "ou-1", model.LarkProfile{LarkUserID: "lkong"}); err != nil {
		t.Fatalf("UpdateLarkBindingProfile() error = %v", err)
	}

	binding, err := users.LarkBinding(ctx, "u1")
	if err != nil {
		t.Fatalf("LarkBinding() error = %v", err)
	}
	if binding != nil {
		t.Fatalf("binding = %+v, want nil: a backfill must not recreate an unbound identity", binding)
	}
}

func TestUpdateLarkBindingProfileEmptyProfileIsNoop(t *testing.T) {
	db := newStoreTestDB(t, &model.UserLarkBindingM{})
	ctx := context.Background()
	users := newUserStore(&store{db: db})
	seedBinding(t, users, "u1", "ou-1")

	if err := users.UpdateLarkBindingProfile(ctx, "u1", "ou-1", model.LarkProfile{}); err != nil {
		t.Fatalf("UpdateLarkBindingProfile() error = %v", err)
	}
	after, err := users.LarkBinding(ctx, "u1")
	if err != nil || after == nil {
		t.Fatalf("read binding: %v", err)
	}
	if after.LarkUserID != "" || after.Name != "" {
		t.Errorf("binding = %+v, want an untouched row", after)
	}
}
