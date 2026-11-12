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

package wshub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckOrigin(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
		origin  string
		want    bool
	}{
		{
			name:   "no allowlist keeps the historical allow-all behaviour",
			origin: "https://evil.example",
			want:   true,
		},
		{
			name:    "a non-browser client has no origin header",
			origins: []string{"https://console.example"},
			want:    true,
		},
		{
			name:    "an exact origin is allowed",
			origins: []string{"https://console.example"},
			origin:  "https://console.example",
			want:    true,
		},
		{
			name:    "a bare host matches any scheme",
			origins: []string{"console.example"},
			origin:  "http://console.example",
			want:    true,
		},
		{
			name:    "a trailing slash in the configuration is ignored",
			origins: []string{"https://console.example/"},
			origin:  "https://console.example",
			want:    true,
		},
		{
			name:    "another origin is refused",
			origins: []string{"https://console.example"},
			origin:  "https://evil.example",
			want:    false,
		},
		{
			name:    "a malformed origin is refused",
			origins: []string{"https://console.example"},
			origin:  "://bad",
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHub(WithAllowedOrigins(tc.origins))
			r := httptest.NewRequest(http.MethodGet, "/v1/ws", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := h.checkOrigin(r); got != tc.want {
				t.Fatalf("checkOrigin = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWithMaxConcurrentFrames(t *testing.T) {
	h := NewHub(WithMaxConcurrentFrames(7))
	if got := cap(h.frameSlots); got != 7 {
		t.Fatalf("frame slot capacity = %d, want 7", got)
	}

	h = NewHub(WithMaxConcurrentFrames(0))
	if got := cap(h.frameSlots); got != defaultMaxConcurrentFrames {
		t.Fatalf("a non-positive limit must keep the default, got %d", got)
	}
}
