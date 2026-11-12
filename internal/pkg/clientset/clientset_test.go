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

package clientset

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"resty.dev/v3"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "short", in: "hello", max: 10, want: "hello"},
		{name: "exact", in: "hello", max: 5, want: "hello"},
		{name: "long", in: "hello world", max: 5, want: "hello..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truncate([]byte(tt.in), tt.max); got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}

func TestTruncateRunesKeepsAValidBoundary(t *testing.T) {
	// "你好世界" is four three-byte runes. A five-byte limit would split the
	// second rune, so the result must back off to the third byte.
	got := TruncateRunes("你好世界", 5)
	want := "你\n… (truncated)"
	if got != want {
		t.Errorf("TruncateRunes = %q, want %q", got, want)
	}
}

func TestErrorFromResponse(t *testing.T) {
	if err := ErrorFromResponse(nil); err == nil {
		t.Fatal("ErrorFromResponse(nil) = nil, want an error")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream broke", http.StatusBadGateway)
	}))
	defer srv.Close()

	resp, err := resty.New().R().Get(srv.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	got := ErrorFromResponse(resp)
	if got == nil {
		t.Fatal("ErrorFromResponse(502) = nil, want an error")
	}
	if !strings.Contains(got.Error(), "502") {
		t.Errorf("error %q does not name the status", got)
	}
}

func TestHealthzSendsTheConfiguredBearer(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewForConfig(&genericoptions.RestyOptions{Endpoint: srv.URL, Token: "secret"})
	if err := client.Healthz(context.Background()); err != nil {
		t.Fatalf("Healthz: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret")
	}
	if gotPath != "/healthz" {
		t.Errorf("path = %q, want /healthz", gotPath)
	}
}

func TestHealthzReportsANon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := NewForConfig(&genericoptions.RestyOptions{Endpoint: srv.URL})
	if err := client.Healthz(context.Background()); err == nil {
		t.Fatal("Healthz = nil, want an error")
	}
}
