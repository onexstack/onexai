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

package userdir

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/chat/model"
)

type fakeStore struct {
	resolve func(ctx context.Context, name string) (*model.UserM, *model.UserLarkBindingM, error)
	calls   int
}

func (f *fakeStore) ResolveByName(
	ctx context.Context, name string,
) (*model.UserM, *model.UserLarkBindingM, error) {
	f.calls++
	if f.resolve == nil {
		return nil, nil, nil
	}
	return f.resolve(ctx, name)
}

func boundUser(username, email, larkID string) func(context.Context, string) (*model.UserM, *model.UserLarkBindingM, error) {
	return func(context.Context, string) (*model.UserM, *model.UserLarkBindingM, error) {
		return &model.UserM{Username: username, Email: email},
			&model.UserLarkBindingM{LarkUserID: larkID}, nil
	}
}

func TestStaticLookup(t *testing.T) {
	m := Static{"go.kong@example.com": "go.kong"}

	for _, name := range []string{"go.kong@example.com", " GO.KONG@example.com "} {
		if got := m.LarkUserID(context.Background(), name); got != "go.kong" {
			t.Errorf("LarkUserID(%q) = %q, want go.kong", name, got)
		}
	}
	if got := m.LarkUserID(context.Background(), "nobody@example.com"); got != "" {
		t.Errorf("LarkUserID(unknown) = %q, want empty", got)
	}
	if got := Static(nil).LarkUserID(context.Background(), "go.kong@example.com"); got != "" {
		t.Errorf("nil Static resolved %q, want empty", got)
	}
}

func TestDirectoryPrefersOverride(t *testing.T) {
	st := &fakeStore{resolve: boundUser("go.kong", "go.kong@example.com", "from-binding")}
	d := NewDirectory(st, Static{"go.kong": "from-config"}, time.Minute, time.Minute)

	if got := d.LarkUserID(context.Background(), "go.kong"); got != "from-config" {
		t.Errorf("LarkUserID = %q, want the explicit override", got)
	}
	if st.calls != 0 {
		t.Errorf("store called %d times, want 0 when the override hits", st.calls)
	}
}

func TestDirectoryChecksOverrideByResolvedAccount(t *testing.T) {
	// The schedule stored the login name, but operations configured the
	// override under the account's email.
	st := &fakeStore{resolve: boundUser("go.kong", "go.kong@example.com", "from-binding")}
	d := NewDirectory(st, Static{"go.kong@example.com": "from-config"}, time.Minute, time.Minute)

	if got := d.LarkUserID(context.Background(), "go.kong"); got != "from-config" {
		t.Errorf("LarkUserID = %q, want the override keyed by the account email", got)
	}
}

func TestDirectoryUsesBinding(t *testing.T) {
	st := &fakeStore{resolve: boundUser("ops.luo", "", "lark-ops")}
	d := NewDirectory(st, nil, time.Minute, time.Minute)

	if got := d.LarkUserID(context.Background(), "ops.luo"); got != "lark-ops" {
		t.Errorf("LarkUserID = %q, want lark-ops", got)
	}
}

func TestDirectoryUnresolved(t *testing.T) {
	cases := []struct {
		name  string
		store Store
		value string
	}{
		{"unknown name", &fakeStore{}, ""},
		{"account without binding", &fakeStore{
			resolve: func(context.Context, string) (*model.UserM, *model.UserLarkBindingM, error) {
				return &model.UserM{Username: "x"}, nil, nil
			},
		}, ""},
		{"binding without lark id", &fakeStore{
			resolve: func(context.Context, string) (*model.UserM, *model.UserLarkBindingM, error) {
				return &model.UserM{Username: "x"}, &model.UserLarkBindingM{}, nil
			},
		}, ""},
		{"nil store", nil, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDirectory(tc.store, nil, time.Minute, time.Minute)
			if got := d.LarkUserID(context.Background(), "someone"); got != tc.value {
				t.Errorf("LarkUserID = %q, want %q", got, tc.value)
			}
		})
	}
}

func TestDirectoryEmptyAndNil(t *testing.T) {
	d := NewDirectory(&fakeStore{}, nil, time.Minute, time.Minute)
	if got := d.LarkUserID(context.Background(), "   "); got != "" {
		t.Errorf("LarkUserID(blank) = %q, want empty", got)
	}
	var nilDir *Directory
	if got := nilDir.LarkUserID(context.Background(), "go.kong"); got != "" {
		t.Errorf("nil directory resolved %q, want empty", got)
	}
}

func TestDirectoryCachesPositiveAndNegative(t *testing.T) {
	st := &fakeStore{resolve: boundUser("ops.luo", "", "lark-ops")}
	now := time.Now()
	d := NewDirectory(st, nil, time.Minute, time.Minute)
	d.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if got := d.LarkUserID(context.Background(), "OPS.LUO"); got != "lark-ops" {
			t.Fatalf("call %d = %q, want lark-ops", i, got)
		}
	}
	if st.calls != 1 {
		t.Errorf("store called %d times, want 1 (case-insensitive cache key)", st.calls)
	}

	unknown := &fakeStore{}
	d = NewDirectory(unknown, nil, time.Minute, time.Minute)
	d.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		d.LarkUserID(context.Background(), "nobody")
	}
	if unknown.calls != 1 {
		t.Errorf("store called %d times for an unknown name, want 1 (negative cache)", unknown.calls)
	}

	// Expiry re-queries.
	now = now.Add(2 * time.Minute)
	d.LarkUserID(context.Background(), "nobody")
	if unknown.calls != 2 {
		t.Errorf("store called %d times after TTL, want 2", unknown.calls)
	}
}

func TestDirectoryDoesNotCacheErrors(t *testing.T) {
	st := &fakeStore{resolve: func(context.Context, string) (*model.UserM, *model.UserLarkBindingM, error) {
		return nil, nil, errors.New("db down")
	}}
	d := NewDirectory(st, nil, time.Minute, time.Minute)

	for i := 0; i < 2; i++ {
		if got := d.LarkUserID(context.Background(), "go.kong"); got != "" {
			t.Errorf("LarkUserID = %q, want empty on error", got)
		}
	}
	if st.calls != 2 {
		t.Errorf("store called %d times, want 2 (errors are not cached)", st.calls)
	}
}
