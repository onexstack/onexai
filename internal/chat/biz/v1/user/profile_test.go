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

package user_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/biz/v1/user"
	"github.com/onexstack/onexai/internal/chat/model"
	"github.com/onexstack/onexai/internal/chat/store"
)

type fakeProfileResolver struct {
	profile model.LarkProfile
	err     error
	calls   int
}

func (f *fakeProfileResolver) Profile(context.Context, string) (model.LarkProfile, error) {
	f.calls++
	return f.profile, f.err
}

type fakeUserStore struct {
	store.UserStore
	bindings []*model.UserLarkBindingM
	updated  map[string]model.LarkProfile
}

func (f *fakeUserStore) ListLarkBindings(context.Context) ([]*model.UserLarkBindingM, error) {
	return f.bindings, nil
}

func (f *fakeUserStore) UpdateLarkBindingProfile(
	_ context.Context, userID, _ string, profile model.LarkProfile,
) error {
	f.updated[userID] = profile
	return nil
}

func TestRunLarkProfileBackfill(t *testing.T) {
	users := &fakeUserStore{
		bindings: []*model.UserLarkBindingM{
			// Complete: nothing to repair.
			{UserID: "complete", OpenID: "ou-complete", LarkUserID: "u-complete", Name: "Complete"},
			// Only the open_id: the row the bind path used to write.
			{UserID: "empty", OpenID: "ou-empty"},
			// A name without a user_id is still incomplete.
			{UserID: "partial", OpenID: "ou-partial", Name: "Partial"},
		},
		updated: map[string]model.LarkProfile{},
	}
	resolver := &fakeProfileResolver{profile: model.LarkProfile{
		LarkUserID: "lark-1", UnionID: "on-1", Name: "Lingfei Kong", AvatarURL: "https://x/640.png",
	}}

	user.RunLarkProfileBackfill(context.Background(), users, resolver, 0)

	if _, ok := users.updated["complete"]; ok {
		t.Error("a complete binding was rewritten")
	}
	if got := users.updated["empty"]; got.LarkUserID != "lark-1" || got.Name != "Lingfei Kong" {
		t.Errorf("empty binding updated with %+v, want the resolved profile", got)
	}
	if got := users.updated["partial"]; got.LarkUserID != "lark-1" {
		t.Errorf("partial binding updated with %+v, want the resolved profile", got)
	}
	if resolver.calls != 2 {
		t.Errorf("resolver called %d times, want 2 (complete rows are skipped)", resolver.calls)
	}
}

func TestRunLarkProfileBackfillIgnoresFailures(t *testing.T) {
	users := &fakeUserStore{
		bindings: []*model.UserLarkBindingM{{UserID: "u1", OpenID: "ou-1"}},
		updated:  map[string]model.LarkProfile{},
	}
	resolver := &fakeProfileResolver{err: errors.New("contact API unavailable")}

	user.RunLarkProfileBackfill(context.Background(), users, resolver, 0)

	if len(users.updated) != 0 {
		t.Errorf("updated = %v, want nothing after a failed lookup", users.updated)
	}
}

func TestRunLarkProfileBackfillStopsOnCancelledContext(t *testing.T) {
	users := &fakeUserStore{
		bindings: []*model.UserLarkBindingM{{UserID: "u1", OpenID: "ou-1"}},
		updated:  map[string]model.LarkProfile{},
	}
	resolver := &fakeProfileResolver{profile: model.LarkProfile{LarkUserID: "lark-1"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		user.RunLarkProfileBackfill(ctx, users, resolver, time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("backfill did not stop after the context was cancelled")
	}
	if len(users.updated) != 0 {
		t.Errorf("updated = %v, want nothing after cancellation", users.updated)
	}
}
