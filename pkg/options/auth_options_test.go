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

package options

import (
	"strings"
	"testing"
)

// TestAuthOptionsRefusesPermissiveModes pins that the two settings which hand
// out an identity without a credential stop the process instead of being
// logged. AlwaysAllow injects an admin identity; anonymous-auth admits a caller
// with no credential at all. Both are refused, and the safe values are not.
func TestAuthOptionsRefusesPermissiveModes(t *testing.T) {
	tests := []struct {
		name    string
		opts    AuthOptions
		refused bool
		marker  string
	}{
		{name: "anonymous-auth is refused", opts: AuthOptions{AnonymousAuth: true}, refused: true, marker: "anonymous-auth"},
		{name: "AlwaysAllow is refused", opts: AuthOptions{AuthorizationMode: AuthorizationModeAlwaysAllow}, refused: true, marker: "AlwaysAllow"},
		{name: "empty mode is the JWT default", opts: AuthOptions{}},
		{name: "AlwaysDeny is allowed", opts: AuthOptions{AuthorizationMode: AuthorizationModeAlwaysDeny}},
		{name: "bypass-aksk alone is not this guard", opts: AuthOptions{BypassAKSK: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.opts.Validate()
			if !tt.refused {
				if len(errs) != 0 {
					t.Fatalf("Validate() = %v, want no error", errs)
				}
				return
			}
			if len(errs) == 0 {
				t.Fatal("Validate() accepted a permissive auth setting")
			}
			if !strings.Contains(errs[0].Error(), tt.marker) {
				t.Errorf("error %q does not name %q", errs[0], tt.marker)
			}
		})
	}
}

// TestValidateJWTKey pins that no built-in value can sign a token: the sample
// config placeholder and the onexstack default are both refused, as is an empty
// key, while an explicitly configured key is accepted.
func TestValidateJWTKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		refused bool
	}{
		{name: "empty", key: "", refused: true},
		{name: "sample-config placeholder", key: "change-me-in-production", refused: true},
		{name: "onexstack code default", key: "CHANGE_ME", refused: true},
		{name: "whitespace-only", key: "   ", refused: true},
		{name: "an explicit key", key: "oai-chat-dev-jwt-key-2f8a1c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJWTKey(tt.key)
			if tt.refused && err == nil {
				t.Fatalf("ValidateJWTKey(%q) = nil, want an error", tt.key)
			}
			if !tt.refused && err != nil {
				t.Fatalf("ValidateJWTKey(%q) = %v, want nil", tt.key, err)
			}
		})
	}
}
