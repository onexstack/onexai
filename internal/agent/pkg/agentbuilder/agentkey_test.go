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

package agentbuilder

import (
	"path/filepath"
	"testing"

	"github.com/onexstack/onexai/internal/agent/pkg/skilldir"
)

func TestNormalizeAgentKey(t *testing.T) {
	cases := []struct {
		name       string
		agentNames []string
		fallback   string
		want       string
	}{
		{name: "empty input falls back", agentNames: nil, fallback: "static-config-name", want: "static-config-name"},
		{name: "all-blank input falls back", agentNames: []string{"", "  "}, fallback: "fallback", want: "fallback"},
		{name: "single agent", agentNames: []string{"bke-agent"}, fallback: "fallback", want: "bke-agent"},
		{name: "duplicates are deduplicated", agentNames: []string{"bke-agent", "bke-agent"}, fallback: "fallback", want: "bke-agent"},
		{name: "surrounding whitespace is trimmed", agentNames: []string{" bke-agent "}, fallback: "fallback", want: "bke-agent"},
		{
			name:       "different input order yields the same key (merge scenario)",
			agentNames: []string{"sre-agent", "bke-agent"},
			fallback:   "fallback",
			want:       "bke-agent.sre-agent",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeAgentKey(tc.agentNames, tc.fallback); got != tc.want {
				t.Errorf("NormalizeAgentKey(%v, %q) = %q, want %q", tc.agentNames, tc.fallback, got, tc.want)
			}
		})
	}
}

// TestNormalizeAgentKey_OrderIndependence pins the guarantee that merging
// multiple agents in any order always resolves to the identical agentKey —
// which is what makes runner-cache reuse and the skills on-disk working
// directory isolation correct for merged agent sets.
func TestNormalizeAgentKey_OrderIndependence(t *testing.T) {
	a := NormalizeAgentKey([]string{"bke-agent", "sre-agent"}, "fallback")
	b := NormalizeAgentKey([]string{"sre-agent", "bke-agent"}, "fallback")
	if a != b {
		t.Errorf("expected order-independent agentKey, got %q vs %q", a, b)
	}
}

// TestIsolatedSkillsDir_MatchesSkillDirSegment pins the invariant that
// Builder.Build's LLM-facing project_root prompt and the SafeLocalBackend
// allow-list (both built from IsolatedSkillsDir) resolve to the exact same
// on-disk directory that skill backends persist files under
// (skill.AgentDirSegment). If this test ever fails, agent-isolated skills
// would be checked out to one directory while the LLM is told (and
// restricted) to look in another — making them silently invisible, which is
// the bug this test guards against.
func TestIsolatedSkillsDir_MatchesSkillAgentDirSegment(t *testing.T) {
	cases := []struct {
		name       string
		skillsDir  string
		configName string
	}{
		{name: "simple agent key", skillsDir: "/var/lib/onexai/skills", configName: "colin"},
		{name: "merged agent key", skillsDir: "/var/lib/onexai/skills", configName: "bke-agent.sre-agent"},
		{name: "traversal is neutralised", skillsDir: "/var/lib/onexai/skills", configName: "../../etc"},
		{name: "fallback shared bucket", skillsDir: "/var/lib/onexai/skills", configName: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsolatedSkillsDir(tc.skillsDir, tc.configName)
			want := filepath.Join(tc.skillsDir, skilldir.AgentDirSegment(tc.configName))
			if got != want {
				t.Errorf("IsolatedSkillsDir(%q, %q) = %q, want %q (must match skill.AgentDirSegment)",
					tc.skillsDir, tc.configName, got, want)
			}
		})
	}
}

// TestAgentDirSegment_NeutralisesTraversal pins that a hostile or malformed
// agent name cannot escape the skills root.
func TestAgentDirSegment_NeutralisesTraversal(t *testing.T) {
	cases := map[string]string{
		"":          "__shared__",
		"   ":       "__shared__",
		"plain":     "plain",
		"a/b":       "a_b",
		`a\b`:       "a_b",
		"../../etc": "____etc",
		"..":        "_",
		"a.b":       "a.b", // dotted agent-set keys are legal and must survive
	}
	for in, want := range cases {
		if got := skilldir.AgentDirSegment(in); got != want {
			t.Errorf("AgentDirSegment(%q) = %q, want %q", in, got, want)
		}
	}
}
