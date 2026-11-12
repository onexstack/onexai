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

package agentcmd

import (
	"testing"

	"k8s.io/apimachinery/pkg/labels"
)

func mustLabels(t *testing.T, selector string) labels.Selector {
	t.Helper()
	parsed, err := labels.Parse(selector)
	if err != nil {
		t.Fatalf("parse %q: %v", selector, err)
	}
	return parsed
}

// A scope is a precondition, not a fourth way of naming agents: it applies
// before the "*" entry, before the name list and before either selector.
func TestScopedRuleOnlyCoversItsOwnNamespace(t *testing.T) {
	snap := buildAgentSnapshot([]AgentInfo{
		{Name: "helper", Namespace: "ops", Labels: map[string]string{"tier": "base"}},
	})
	agent := []string{"helper"}

	tests := []struct {
		name string
		rule compiledRule
		want bool
	}{
		{
			name: "unscoped rule covers any namespace",
			rule: compiledRule{allAgents: true},
			want: true,
		},
		{
			name: "scope matches",
			rule: compiledRule{scope: "ops", allAgents: true},
			want: true,
		},
		{
			name: "scope does not match",
			rule: compiledRule{scope: "other", allAgents: true},
			want: false,
		},
		{
			name: "a named agent in another namespace is still refused",
			rule: compiledRule{scope: "other", agents: map[string]struct{}{"helper": {}}},
			want: false,
		},
		{
			name: "a label match in another namespace is still refused",
			rule: compiledRule{
				scope:         "other",
				agentSelector: mustLabels(t, "tier=base"),
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.covers(agent, snap); got != tc.want {
				t.Errorf("covers() = %v, want %v", got, tc.want)
			}
		})
	}
}

// An agent the snapshot does not know cannot be placed in a namespace, so a
// scoped rule fails closed rather than matching by accident.
func TestScopedRuleFailsClosedForAnUnknownAgent(t *testing.T) {
	snap := buildAgentSnapshot(nil)
	rule := compiledRule{scope: "ops", allAgents: true}
	if rule.covers([]string{"ghost"}, snap) {
		t.Error("a scoped rule covered an agent the snapshot does not know")
	}
}
