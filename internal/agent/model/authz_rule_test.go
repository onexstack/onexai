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

package model

import "testing"

// TestAuthzRuleEnabledFollowsStatus is the one assertion the store's
// authz-rule test kept when its round-trip half went away with the record
// layer: the rest of that file pinned how a list became a JSON column, which
// is now StringList's business (see sqltypes_test.go).
func TestAuthzRuleEnabledFollowsStatus(t *testing.T) {
	if !(AuthzRuleM{Status: StatusEnabled}).Enabled() {
		t.Error("an enabled rule must report Enabled()==true")
	}
	if (AuthzRuleM{Status: StatusDisabled}).Enabled() {
		t.Error("a disabled rule must report Enabled()==false")
	}
}

// TestEnabledFollowsStatusForEveryResourceType is the same rule for the types
// that carry a Status column, because a type that stopped honouring "disabled"
// would keep serving records an operator has switched off.
func TestEnabledFollowsStatusForEveryResourceType(t *testing.T) {
	enabled := []struct {
		name string
		got  bool
	}{
		{"agent", AgentM{Status: StatusEnabled}.Enabled()},
		{"skill", SkillM{Status: StatusEnabled}.Enabled()},
		{"mcp", MCPM{Status: StatusEnabled}.Enabled()},
		{"knowledge_base", KnowledgeBaseM{Status: StatusEnabled}.Enabled()},
		{"prompt", PromptM{Status: StatusEnabled}.Enabled()},
		{"model", ModelM{Status: StatusEnabled}.Enabled()},
	}
	for _, tc := range enabled {
		if !tc.got {
			t.Errorf("%s: an enabled record must report Enabled()==true", tc.name)
		}
	}

	disabled := []struct {
		name string
		got  bool
	}{
		{"agent", AgentM{Status: StatusDisabled}.Enabled()},
		{"skill", SkillM{Status: StatusDisabled}.Enabled()},
		{"mcp", MCPM{Status: StatusDisabled}.Enabled()},
		{"knowledge_base", KnowledgeBaseM{Status: StatusDisabled}.Enabled()},
		{"prompt", PromptM{Status: StatusDisabled}.Enabled()},
		{"model", ModelM{Status: StatusDisabled}.Enabled()},
	}
	for _, tc := range disabled {
		if tc.got {
			t.Errorf("%s: a disabled record must report Enabled()==false", tc.name)
		}
	}
}
