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

	"github.com/onexstack/onexai/internal/chat/pkg/roleset"
)

// TestRoleGateReadsTheConfiguredLists pins that SRE/admin membership is decided
// by the two lists, not handed out unconditionally. This is the gate's only
// source: ValidateCommands turns a false answer into the "requires SRE role"
// refusal, so a method that answered true for everyone would open every
// x-sre-only and x-admin-only command to every caller.
//
// The two lists are deliberately different: an admin is not automatically an SRE
// (the roles gate different commands), so ou_admin must pass IsAdminUser and
// fail IsSREUser — and ou_sre must pass both, because the cache adds every SRE to
// the admin list.
func TestRoleGateReadsTheConfiguredLists(t *testing.T) {
	sr := &StaticRuntime{
		Roles: roleset.NewStatic(
			map[string]struct{}{"ou_sre": {}},
			map[string]struct{}{"ou_admin": {}},
		),
	}

	tests := []struct {
		name  string
		check func(string) bool
		id    string
		want  bool
	}{
		{"a listed SRE is an SRE", sr.IsSREUser, "ou_sre", true},
		{"an admin who is not listed as SRE is not an SRE", sr.IsSREUser, "ou_admin", false},
		{"a stranger is not an SRE", sr.IsSREUser, "ou_stranger", false},
		{"an empty id is not an SRE", sr.IsSREUser, "", false},

		{"a listed SRE is an admin", sr.IsAdminUser, "ou_sre", true},
		{"a listed admin is an admin", sr.IsAdminUser, "ou_admin", true},
		{"a stranger is not an admin", sr.IsAdminUser, "ou_stranger", false},
		{"an empty id is not an admin", sr.IsAdminUser, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.check(tt.id); got != tt.want {
				t.Errorf("role check(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

// TestRoleGateFailsClosedOnEmptyListsAndMissingRuntime pins the two ways the
// gate must not open. An empty list means "nobody", not "everybody": a
// deployment that configured no roles has no one who may run the restricted
// commands. A nil runtime is a server that failed to build, not a user who has
// no role, and answering true there would grant admin to a broken deployment.
func TestRoleGateFailsClosedOnEmptyListsAndMissingRuntime(t *testing.T) {
	empty := &StaticRuntime{}
	if empty.IsSREUser("ou_sre") {
		t.Error("an empty SRE list admitted a user")
	}
	if empty.IsAdminUser("ou_admin") {
		t.Error("an empty admin list admitted a user")
	}

	var nilRuntime *StaticRuntime
	if nilRuntime.IsSREUser("ou_sre") {
		t.Error("IsSREUser() on a nil runtime = true, want false")
	}
	if nilRuntime.IsAdminUser("ou_sre") {
		t.Error("IsAdminUser() on a nil runtime = true, want false")
	}
}
