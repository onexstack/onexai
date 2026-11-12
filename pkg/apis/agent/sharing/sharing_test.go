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

package sharing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAllowsGrant(t *testing.T) {
	agent := Ref{Group: "agent.onexai.io", Kind: "Agent", Namespace: "helper-public", Name: "k8s-alert-rca"}
	skill := Ref{Group: "agent.onexai.io", Kind: "Skill", Namespace: "default", Name: "k8s-debug"}
	otherSkill := Ref{Group: "agent.onexai.io", Kind: "Skill", Namespace: "default", Name: "other"}

	matching := Grant{
		Namespace:     "default",
		FromGroup:     "agent.onexai.io",
		FromKind:      "Agent",
		FromNamespace: "helper-public",
		ToGroup:       "agent.onexai.io",
		ToKind:        "Skill",
		ToName:        "k8s-debug",
	}

	tests := []struct {
		name   string
		grants []Grant
		from   Ref
		to     Ref
		want   bool
	}{
		{name: "same namespace needs no grant", grants: nil, from: agent, to: Ref{Group: "agent.onexai.io", Kind: "Skill", Namespace: "helper-public", Name: "local"}, want: true},
		{name: "cross namespace with matching grant", grants: []Grant{matching}, from: agent, to: skill, want: true},
		{name: "cross namespace without a grant", grants: nil, from: agent, to: skill, want: false},
		{name: "grant for another target name", grants: []Grant{matching}, from: agent, to: otherSkill, want: false},
		{name: "empty target name covers every name", grants: []Grant{func() Grant { g := matching; g.ToName = ""; return g }()}, from: agent, to: otherSkill, want: true},
		{name: "grant from another namespace", grants: []Grant{func() Grant { g := matching; g.FromNamespace = "team-b"; return g }()}, from: agent, to: skill, want: false},
		{name: "grant from another kind", grants: []Grant{func() Grant { g := matching; g.FromKind = "AgentBinding"; return g }()}, from: agent, to: skill, want: false},
		{name: "grant for another target kind", grants: []Grant{func() Grant { g := matching; g.ToKind = "MCPServer"; return g }()}, from: agent, to: skill, want: false},
		{name: "grant in another namespace", grants: []Grant{func() Grant { g := matching; g.Namespace = "team-a"; return g }()}, from: agent, to: skill, want: false},
		{name: "empty from namespace normalizes to default", grants: []Grant{func() Grant { g := matching; g.FromNamespace = ""; return g }()}, from: Ref{Group: "agent.onexai.io", Kind: "Agent", Namespace: "helper-public"}, to: skill, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AllowsGrant(tc.grants, tc.from, tc.to))
		})
	}
}

func TestGrantIndex(t *testing.T) {
	agent := Ref{Group: GroupName, Kind: "Agent", Namespace: "helper-public", Name: "k8s-alert-rca"}
	skill := Ref{Group: GroupName, Kind: "Skill", Namespace: "default", Name: "k8s-debug"}
	local := Ref{Group: GroupName, Kind: "Skill", Namespace: "helper-public", Name: "local"}
	index := NewGrantIndex([]Grant{{
		Namespace:     "default",
		FromGroup:     GroupName,
		FromKind:      "Agent",
		FromNamespace: "helper-public",
		ToGroup:       GroupName,
		ToKind:        "Skill",
		ToName:        "k8s-debug",
	}})

	assert.True(t, index.Allows(agent, skill))
	assert.True(t, index.Allows(agent, local), "same namespace needs no grant")
	assert.False(t, (*GrantIndex)(nil).Allows(agent, skill))
	assert.True(t, (*GrantIndex)(nil).Allows(agent, local))
}

func TestSplitRef(t *testing.T) {
	ns, name := SplitRef("helper-public/onexai")
	assert.Equal(t, "helper-public", ns)
	assert.Equal(t, "onexai", name)

	ns, name = SplitRef("onexai")
	assert.Empty(t, ns)
	assert.Equal(t, "onexai", name)
}

func TestResolve(t *testing.T) {
	assert.Equal(t, Ref{Namespace: "default", Name: "x"},
		Resolve("default", "x"), "a bare name belongs to the referrer")
	assert.Equal(t, Ref{Namespace: "team-b", Name: "x"},
		Resolve("default", "team-b/x"), "a qualified name keeps its namespace")
	assert.Equal(t, Ref{Group: GroupName, Kind: KindSkill, Namespace: DefaultNamespace, Name: "x"},
		ResolveKind("", KindSkill, "x"), "empty referrer normalizes to the default namespace")
}

func TestShareable(t *testing.T) {
	for _, kind := range []string{KindMCPServer, KindSkill, KindKnowledgeBase, KindPrompt, KindMemory, KindModel} {
		assert.True(t, Shareable(kind), "%s must be shareable", kind)
	}
	for _, kind := range []string{KindCustomCommand, KindAgent, "Webhook", "RuntimeProfile", ""} {
		assert.False(t, Shareable(kind), "%s must be same-namespace only", kind)
	}
}

func TestGrantIndexAllowsAll(t *testing.T) {
	index := NewGrantIndex([]Grant{{
		Namespace:     "team-b",
		FromGroup:     GroupName,
		FromKind:      KindAgent,
		FromNamespace: "team-a",
		ToGroup:       GroupName,
		ToKind:        KindSkill,
		ToName:        "k8s-debug",
	}})
	to := Ref{Group: GroupName, Kind: KindSkill, Namespace: "team-b", Name: "k8s-debug"}

	assert.True(t, index.AllowsAll([]string{"team-b"}, to), "same namespace is enough")
	assert.True(t, index.AllowsAll([]string{"team-a"}, to), "a grant admits its referrer")
	assert.False(t, index.AllowsAll([]string{"team-a", "team-c"}, to),
		"every referrer must be admitted, not just one")
	assert.False(t, index.AllowsAll(nil, to), "no referrer admits nothing")
}
