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

package runtime

import (
	"testing"

	"github.com/onexstack/onexai/internal/chat/model"
)

// TestSubjectFor pins the identity the agent service is asked about: the account
// a speaker is bound to, or their tenant identity when there is no binding.
//
// Both callers — the scheduler, which puts it into the run, and /agent, which
// asks in advance — go through this function, so this is the single statement of
// the rule. A binding with no canonical identity is the unbound case rather than
// a third answer: the column is what the rules are keyed by, and an empty one
// names nobody.
func TestSubjectFor(t *testing.T) {
	tests := []struct {
		name         string
		tenantUserID string
		binding      *model.PeerAgentM
		want         string
	}{
		{
			name:         "a bound speaker is authorized as the bound account",
			tenantUserID: "go.kong",
			binding:      &model.PeerAgentM{Canonical: "user-mkc1kj"},
			want:         "user-mkc1kj",
		},
		{
			name:         "an unbound speaker is authorized as their tenant identity",
			tenantUserID: "go.kong",
			binding:      nil,
			want:         "go.kong",
		},
		{
			name:         "a binding with no account falls back to the tenant identity",
			tenantUserID: "go.kong",
			binding:      &model.PeerAgentM{},
			want:         "go.kong",
		},
		{
			name:         "an unidentified speaker stays unidentified",
			tenantUserID: "",
			binding:      nil,
			want:         "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SubjectFor(tt.tenantUserID, tt.binding); got != tt.want {
				t.Errorf("SubjectFor(%q, %+v) = %q, want %q", tt.tenantUserID, tt.binding, got, tt.want)
			}
		})
	}
}
