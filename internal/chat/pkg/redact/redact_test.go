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

package redact

import "testing"

func TestSecrets(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"no secrets here", "no secrets here"},
		{"https://x/api?app_secret=abc123&foo=bar", "https://x/api?app_secret=***&foo=bar"},
		{"token=deadbeef", "token=***"},
		{"password=hunter2 token=zzz", "password=*** token=***"},
		{"API_KEY=topsecret", "API_KEY=***"},
	}
	for _, tt := range tests {
		if got := Secrets(tt.in); got != tt.want {
			t.Errorf("Secrets(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSecret(t *testing.T) {
	if got := Secret("x"); got != "***" {
		t.Errorf("Secret(x) = %q, want ***", got)
	}
	if got := Secret(""); got != "" {
		t.Errorf("Secret(\"\") = %q, want empty", got)
	}
}
