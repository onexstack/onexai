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

package dnswhitelist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const forwardConf = `
# dnsmasq forward.conf
server=/api.example.com/10.0.0.1
server=/.svc.local/10.0.0.2
`

func TestNewLoadsAndMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(forwardConf))
	}))
	defer srv.Close()

	w, err := New(context.Background(), srv.URL, "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, tc := range []struct {
		domain string
		want   bool
		rule   string
	}{
		{"api.example.com", true, "api.example.com"},
		{"API.Example.com.", true, "api.example.com"},
		{"other.example.com", false, ""},
		{"a.b.svc.local", true, ".svc.local"},
		{"svc.local", true, ".svc.local"},
		{"notsvc.local", false, ""},
	} {
		got := w.Check(tc.domain)
		if got.InWhitelist != tc.want || got.MatchedRule != tc.rule {
			t.Errorf("Check(%q) = %+v, want in=%v rule=%q", tc.domain, got, tc.want, tc.rule)
		}
	}

	if w.LastUpdated().IsZero() {
		t.Error("LastUpdated is zero after a successful load")
	}
}

func TestNewFailsWhenTheFirstLoadFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := New(context.Background(), srv.URL, "", 0); err == nil {
		t.Fatal("New must fail when the whitelist cannot be read")
	}
}

func TestRefreshFailureKeepsThePreviousList(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(forwardConf))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := New(ctx, srv.URL, "", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loadedAt := w.LastUpdated()

	fail.Store(true)
	time.Sleep(100 * time.Millisecond)

	if got := w.Check("api.example.com"); !got.InWhitelist {
		t.Fatalf("a failed refresh must keep the previous list, got %+v", got)
	}
	if got := w.LastUpdated(); !got.Equal(loadedAt) {
		t.Errorf("LastUpdated moved after a failed refresh: %v -> %v", loadedAt, got)
	}
}
