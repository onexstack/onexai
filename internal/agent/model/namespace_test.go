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

func TestNormalizeNamespace(t *testing.T) {
	if got := NormalizeNamespace(""); got != DefaultNamespace {
		t.Fatalf("empty namespace: got %q, want %q", got, DefaultNamespace)
	}
	if got := NormalizeNamespace("team-a"); got != "team-a" {
		t.Fatalf("named namespace: got %q, want %q", got, "team-a")
	}
}

func TestGetNamespaceNormalizesEachResource(t *testing.T) {
	cases := []struct {
		name string
		got  string
	}{
		{"agent", AgentM{}.GetNamespace()},
		{"skill", SkillM{}.GetNamespace()},
		{"mcp", MCPM{}.GetNamespace()},
		{"knowledgebase", KnowledgeBaseM{}.GetNamespace()},
		{"prompt", PromptM{}.GetNamespace()},
	}
	for _, tc := range cases {
		if tc.got != DefaultNamespace {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, DefaultNamespace)
		}
	}
	if got := (AgentM{Namespace: "team-a"}).GetNamespace(); got != "team-a" {
		t.Errorf("agent with a namespace: got %q, want team-a", got)
	}
}
