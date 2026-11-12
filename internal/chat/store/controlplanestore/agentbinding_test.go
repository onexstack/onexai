// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

	"github.com/onexstack/onexstack/pkg/ptr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func TestAgentBindingRuleFromCREmptySelectorMatchesAll(t *testing.T) {
	row := agentBindingRuleFromCR(&agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: agentv1alpha1.AgentBindingSpec{
			Selector: &metav1.LabelSelector{},
			Commands: []string{"bind", "oncall"},
		},
	})

	if row.Name != AgentBindingRulePrefix+"platform" {
		t.Errorf("Name = %q, want %q", row.Name, AgentBindingRulePrefix+"platform")
	}
	if !row.Enabled() {
		t.Error("a binding with no explicit enabled field must be enabled")
	}
	if got := decodeList(row.Agents); len(got) != 1 || got[0] != allAgentsSentinel {
		t.Errorf("Agents = %v, want [%s]", got, allAgentsSentinel)
	}
	if got := decodeList(row.Commands); len(got) != 2 {
		t.Errorf("Commands = %v, want 2 entries", got)
	}
}

func TestAgentBindingRuleFromCRNamedAndLabelled(t *testing.T) {
	row := agentBindingRuleFromCR(&agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "sre"},
		Spec: agentv1alpha1.AgentBindingSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "sre"}},
			Agents:   []string{"helper"},
			Commands: []string{"bind"},
		},
	})

	if row.AgentSelector != "team=sre" {
		t.Errorf("AgentSelector = %q, want team=sre", row.AgentSelector)
	}
	if got := decodeList(row.Agents); len(got) != 1 || got[0] != "helper" {
		t.Errorf("Agents = %v, want [helper]", got)
	}
}

func TestAgentBindingRuleFromCRDisabled(t *testing.T) {
	row := agentBindingRuleFromCR(&agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "off"},
		Spec: agentv1alpha1.AgentBindingSpec{
			Enabled:  ptr.To(false),
			Selector: &metav1.LabelSelector{},
			Commands: []string{"bind"},
		},
	})

	if row.Enabled() {
		t.Error("an explicitly disabled binding must not grant commands")
	}
}

// An empty selector already covers every agent in the namespace, so naming
// agents alongside it cannot narrow the rule. The controller treats the two as
// a union; the projection must agree.
func TestAgentBindingRuleFromCREmptySelectorWinsOverNamedAgents(t *testing.T) {
	row := agentBindingRuleFromCR(&agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: agentv1alpha1.AgentBindingSpec{
			Selector: &metav1.LabelSelector{},
			Agents:   []string{"helper"},
			Commands: []string{"bind"},
		},
	})

	got := decodeList(row.Agents)
	if len(got) != 1 || got[0] != allAgentsSentinel {
		t.Errorf("Agents = %v, want [%s]", got, allAgentsSentinel)
	}
}

// A binding is namespace-scoped, so the rule it projects must be too. Without
// this the resolver, which matches agents by name across namespaces, would let
// a binding in one namespace grant commands to another namespace's agent.
func TestAgentBindingRuleIsScopedToItsNamespace(t *testing.T) {
	row := agentBindingRuleFromCR(&agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: "ops"},
		Spec: agentv1alpha1.AgentBindingSpec{
			Selector: &metav1.LabelSelector{},
			Commands: []string{"bind"},
		},
	})

	if row.Scope != "ops" {
		t.Errorf("Scope = %q, want ops", row.Scope)
	}
}
