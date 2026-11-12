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

package controlplanestore

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onexstack/onexai/internal/chat/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func dutyBinding() *agentv1alpha1.ClusterAgentBinding {
	return &agentv1alpha1.ClusterAgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "oncall-duty-all"},
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Namespaces:    []string{agentv1alpha1.AllNamespaces},
			AgentSelector: &metav1.LabelSelector{},
			Commands:      []string{"oncall", "duty", "close"},
		},
	}
}

func TestClusterBindingWildcardGrantsEveryAgentInEveryNamespace(t *testing.T) {
	rows := clusterAgentBindingRulesFromCR(dutyBinding())
	if len(rows) != 1 {
		t.Fatalf("got %d rules, want 1: %+v", len(rows), rows)
	}

	row := rows[0]
	if row.Name != ClusterAgentBindingRulePrefix+"oncall-duty-all" {
		t.Errorf("Name = %q", row.Name)
	}
	if row.Scope != "" {
		t.Errorf("Scope = %q, a wildcard rule must reach every namespace", row.Scope)
	}
	if got := decodeList(row.Agents); len(got) != 1 || got[0] != allAgentsSentinel {
		t.Errorf("Agents = %v, want [%s]", got, allAgentsSentinel)
	}
	if got := decodeList(row.Commands); len(got) != 3 {
		t.Errorf("Commands = %v, want 3 entries", got)
	}
	if !row.Enabled() {
		t.Error("an enabled binding must produce an enabled rule")
	}
}

func TestClusterBindingProjectsTheCommandSelector(t *testing.T) {
	binding := dutyBinding()
	binding.Spec.Commands = nil
	binding.Spec.CommandSelector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      "agent.onexstack.io/group",
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{"duty"},
		}},
	}

	rows := clusterAgentBindingRulesFromCR(binding)
	if len(rows) != 1 {
		t.Fatalf("a selector-only binding must still produce a rule: %+v", rows)
	}
	if got := rows[0].CommandSelector; got != "agent.onexstack.io/group in (duty)" {
		t.Errorf("CommandSelector = %q", got)
	}
	if got := decodeList(rows[0].Commands); len(got) != 0 {
		t.Errorf("Commands = %v, want none", got)
	}
}

func TestClusterBindingDropsAnEmptyCommandSelector(t *testing.T) {
	binding := dutyBinding()
	binding.Spec.Commands = nil
	binding.Spec.CommandSelector = &metav1.LabelSelector{}

	if rows := clusterAgentBindingRulesFromCR(binding); len(rows) != 0 {
		t.Fatalf("an empty selector grants nothing and must not produce a rule: %+v", rows)
	}
}

func TestClusterBindingWildcardWithLabelSelectorHasNoScope(t *testing.T) {
	binding := dutyBinding()
	binding.Spec.AgentSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "sre"}}

	rows := clusterAgentBindingRulesFromCR(binding)
	if len(rows) != 1 {
		t.Fatalf("got %d rules, want 1: %+v", len(rows), rows)
	}
	if rows[0].AgentSelector != "team=sre" {
		t.Errorf("AgentSelector = %q, want team=sre", rows[0].AgentSelector)
	}
	if rows[0].Scope != "" {
		t.Errorf("Scope = %q, want empty", rows[0].Scope)
	}
}

func TestClusterBindingWildcardNamedAgentsAreScopedPerNamespace(t *testing.T) {
	binding := dutyBinding()
	binding.Spec.AgentSelector = nil
	binding.Spec.Agents = []agentv1alpha1.Reference{
		{Namespace: "team-a", Name: "helper"},
		{Namespace: "team-b", Name: "helper"},
	}

	rows := clusterAgentBindingRulesFromCR(binding)
	if len(rows) != 2 {
		t.Fatalf("got %d rules, want 2: %+v", len(rows), rows)
	}

	byScope := map[string]*model.AgentCommandM{}
	for _, row := range rows {
		byScope[row.Scope] = row
	}
	for _, namespace := range []string{"team-a", "team-b"} {
		row, ok := byScope[namespace]
		if !ok {
			t.Fatalf("no rule scoped to %q", namespace)
		}
		if got := decodeList(row.Agents); len(got) != 1 || got[0] != "helper" {
			t.Errorf("scope %q: Agents = %v, want [helper]", namespace, got)
		}
	}
}

func TestClusterBindingExplicitNamespacesExpandToOneRuleEach(t *testing.T) {
	binding := dutyBinding()
	binding.Spec.Namespaces = []string{"team-b", "team-a", "team-a"}

	rows := clusterAgentBindingRulesFromCR(binding)
	if len(rows) != 2 {
		t.Fatalf("got %d rules, want 2 (deduplicated, in order): %+v", len(rows), rows)
	}
	if rows[0].Scope != "team-a" || rows[1].Scope != "team-b" {
		t.Errorf("scopes = [%q, %q], want sorted [team-a team-b]", rows[0].Scope, rows[1].Scope)
	}
	for _, row := range rows {
		if got := decodeList(row.Agents); len(got) != 1 || got[0] != allAgentsSentinel {
			t.Errorf("scope %q: Agents = %v, want [%s]", row.Scope, got, allAgentsSentinel)
		}
	}
}

func TestClusterBindingExplicitNamespacesUnionSelectorAndNames(t *testing.T) {
	binding := dutyBinding()
	binding.Spec.Namespaces = []string{"team-a", "team-b"}
	binding.Spec.AgentSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "sre"}}
	binding.Spec.Agents = []agentv1alpha1.Reference{
		{Namespace: "team-a", Name: "named-a"},
		{Namespace: "team-b", Name: "named-b"},
		// Outside every target namespace: must be dropped.
		{Namespace: "team-c", Name: "named-c"},
	}

	rows := clusterAgentBindingRulesFromCR(binding)
	if len(rows) != 2 {
		t.Fatalf("got %d rules, want 2: %+v", len(rows), rows)
	}

	want := map[string][]string{
		"team-a": {"named-a"},
		"team-b": {"named-b"},
	}
	for _, row := range rows {
		if row.AgentSelector != "team=sre" {
			t.Errorf("scope %q: AgentSelector = %q, want team=sre", row.Scope, row.AgentSelector)
		}
		got := decodeList(row.Agents)
		if len(got) != 1 || got[0] != want[row.Scope][0] {
			t.Errorf("scope %q: Agents = %v, want %v", row.Scope, got, want[row.Scope])
		}
	}
}

func TestClusterBindingWithoutCommandsOrWhenDisabledProjectsNothing(t *testing.T) {
	noCommands := dutyBinding()
	noCommands.Spec.Commands = nil
	if rows := clusterAgentBindingRulesFromCR(noCommands); len(rows) != 0 {
		t.Errorf("a binding with no commands granted %d rules, want 0", len(rows))
	}

	disabled := false
	off := dutyBinding()
	off.Spec.Enabled = &disabled
	if rows := clusterAgentBindingRulesFromCR(off); len(rows) != 0 {
		t.Errorf("a disabled binding granted %d rules, want 0", len(rows))
	}
}
