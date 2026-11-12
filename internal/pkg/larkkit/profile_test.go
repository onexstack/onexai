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

package larkkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

type contactServer struct {
	ts       *httptest.Server
	requests atomic.Int32
	userID   string
	body     string
}

func newContactServer(t *testing.T, body string) *contactServer {
	t.Helper()
	s := &contactServer{body: body}
	s.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			writeJSON(w, `{"code":0,"msg":"ok","tenant_access_token":"t-1","expire":7200}`)
			return
		}
		if strings.Contains(r.URL.Path, "/open-apis/contact/v3/users/") {
			s.requests.Add(1)
			if r.URL.Query().Get("user_id_type") != "open_id" {
				writeJSON(w, `{"code":400,"msg":"wrong user_id_type"}`)
				return
			}
			writeJSON(w, s.body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(s.ts.Close)
	return s
}

func (s *contactServer) client() *lark.Client {
	return lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(s.ts.URL))
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestProfileResolverFullRecord(t *testing.T) {
	server := newContactServer(t, `{
		"code":0,"msg":"ok","data":{"user":{
			"user_id":"lkong","union_id":"on-1","open_id":"ou-1","name":"Lingfei Kong",
			"avatar":{"avatar_72":"https://x/72.png","avatar_240":"","avatar_640":"https://x/640.png"}
		}}
	}`)
	resolver := NewProfileResolver(server.client(), 0)

	profile, err := resolver.Profile(context.Background(), "ou-1")
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	if profile.UserID != "lkong" || profile.UnionID != "on-1" ||
		profile.OpenID != "ou-1" || profile.Name != "Lingfei Kong" {
		t.Fatalf("Profile() = %+v, want the full contact record", profile)
	}
	if profile.AvatarURL != "https://x/640.png" {
		t.Errorf("AvatarURL = %q, want avatar_640", profile.AvatarURL)
	}

	// The second lookup is served from cache.
	if _, err := resolver.Profile(context.Background(), "ou-1"); err != nil {
		t.Fatalf("second Profile() error = %v", err)
	}
	if got := server.requests.Load(); got != 1 {
		t.Errorf("contact API called %d times, want 1 (cache hit)", got)
	}
	if resolver.Calls() != 1 {
		t.Errorf("Calls() = %d, want 1", resolver.Calls())
	}
}

func TestProfileResolverAvatarFallback(t *testing.T) {
	server := newContactServer(t, `{
		"code":0,"msg":"ok","data":{"user":{
			"user_id":"lkong","name":"Kong","avatar":{"avatar_72":"https://x/72.png","avatar_640":""}
		}}
	}`)
	resolver := NewProfileResolver(server.client(), 0)

	profile, err := resolver.Profile(context.Background(), "ou-1")
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	if profile.AvatarURL != "https://x/72.png" {
		t.Errorf("AvatarURL = %q, want the avatar_72 fallback", profile.AvatarURL)
	}
}

func TestProfileResolverPartialRecord(t *testing.T) {
	// No user_id: the app cannot read user IDs, but the name is still useful.
	server := newContactServer(t, `{
		"code":0,"msg":"ok","data":{"user":{"open_id":"ou-1","name":"Kong"}}
	}`)
	resolver := NewProfileResolver(server.client(), 0)

	profile, err := resolver.Profile(context.Background(), "ou-1")
	if err != nil {
		t.Fatalf("Profile() error = %v, want a partial success", err)
	}
	if profile.UserID != "" {
		t.Errorf("UserID = %q, want empty", profile.UserID)
	}
	if profile.Name != "Kong" {
		t.Errorf("Name = %q, want Kong", profile.Name)
	}
}

func TestProfileResolverAPIError(t *testing.T) {
	server := newContactServer(t, `{"code":41050,"msg":"no permission"}`)
	resolver := NewProfileResolver(server.client(), 0)

	if _, err := resolver.Profile(context.Background(), "ou-1"); err == nil {
		t.Fatal("Profile() error = nil, want an API error")
	}
	// Negative answers are cached briefly: one API call for two lookups.
	if _, err := resolver.Profile(context.Background(), "ou-1"); err == nil {
		t.Fatal("second Profile() error = nil, want the cached error")
	}
	if got := server.requests.Load(); got != 1 {
		t.Errorf("contact API called %d times, want 1 (negative cache)", got)
	}
}

func TestProfileResolverEmptyInput(t *testing.T) {
	resolver := NewProfileResolver(nil, 0)
	if _, err := resolver.Profile(context.Background(), "ou-1"); err == nil {
		t.Fatal("Profile() with a nil client returned no error")
	}

	var nilResolver *ProfileResolver
	if _, err := nilResolver.Profile(context.Background(), "ou-1"); err == nil {
		t.Fatal("nil resolver returned no error")
	}
}

func TestIdentityResolverStillUsesContactAPI(t *testing.T) {
	server := newContactServer(t, `{
		"code":0,"msg":"ok","data":{"user":{"user_id":"lkong","open_id":"ou-1","name":"Kong"}}
	}`)
	resolver := NewIdentityResolver(server.client(), 0)

	id, err := resolver.UserID(context.Background(), "ou-1")
	if err != nil {
		t.Fatalf("UserID() error = %v", err)
	}
	if id != "lkong" {
		t.Errorf("UserID() = %q, want lkong", id)
	}
}
