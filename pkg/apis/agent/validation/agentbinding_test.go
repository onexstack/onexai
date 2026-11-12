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

package validation

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func TestValidateAgentBindingRequiresSelector(t *testing.T) {
	spec := &agent.AgentBindingSpec{Commands: []string{"bind"}}
	errs := ValidateAgentBinding(spec, field.NewPath("spec"))
	if len(errs) == 0 {
		t.Fatal("a binding without a selector must be rejected")
	}
}

func TestValidateAgentBindingAcceptsCrossNamespaceReference(t *testing.T) {
	spec := &agent.AgentBindingSpec{
		Selector: &metav1.LabelSelector{},
		Skills:   []agent.Reference{{Name: "lint", Namespace: "other"}},
	}
	if errs := ValidateAgentBinding(spec, field.NewPath("spec")); len(errs) != 0 {
		t.Fatalf("a shareable reference may cross namespaces: %v", errs)
	}
}

func TestValidateAgentBindingRejectsCrossNamespaceCustomCommand(t *testing.T) {
	spec := &agent.AgentBindingSpec{
		Selector:       &metav1.LabelSelector{},
		CustomCommands: []agent.Reference{{Name: "triage", Namespace: "other"}},
	}
	errs := ValidateAgentBinding(spec, field.NewPath("spec"))
	if len(errs) == 0 {
		t.Fatal("a custom command runs with the caller's authority and must stay in the binding's namespace")
	}
}

func TestValidateAgentBindingAcceptsTheCommonShape(t *testing.T) {
	spec := &agent.AgentBindingSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"platform": "true"}},
		Agents:   []string{"helper"},
		Skills:   []agent.Reference{{Name: "lint"}},
		Commands: []string{"bind", "oncall"},
	}
	if errs := ValidateAgentBinding(spec, field.NewPath("spec")); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestValidateAgentBindingRejectsAnEmptyCommandSelector(t *testing.T) {
	spec := &agent.AgentBindingSpec{
		Selector:        &metav1.LabelSelector{},
		CommandSelector: &metav1.LabelSelector{},
	}
	errs := ValidateAgentBinding(spec, field.NewPath("spec"))
	if len(errs) == 0 {
		t.Fatal("an empty command selector matches every command and must be rejected")
	}
}

func TestValidateAgentBindingAcceptsACommandSelector(t *testing.T) {
	spec := &agent.AgentBindingSpec{
		Selector: &metav1.LabelSelector{},
		CommandSelector: &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      "agent.onexstack.io/group",
				Operator: metav1.LabelSelectorOpIn,
				Values:   []string{"duty"},
			}},
		},
	}
	if errs := ValidateAgentBinding(spec, field.NewPath("spec")); len(errs) != 0 {
		t.Fatalf("a group selector is a valid command grant: %v", errs)
	}
}

func TestCanonicalizeAgentBindingSpec(t *testing.T) {
	spec := &agent.AgentBindingSpec{
		Agents:         []string{"b", "a", "a"},
		Skills:         []agent.Reference{{Name: "z"}, {Name: "a"}, {Name: "a"}},
		KnowledgeBases: []agent.Reference{{Name: "kb"}},
		Commands:       []string{"two", "one", "two"},
	}

	CanonicalizeAgentBindingSpec(spec)

	if got := spec.Agents; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("Agents = %v, want [a b]", got)
	}
	if got := spec.Skills; len(got) != 2 || got[0].Name != "a" || got[1].Name != "z" {
		t.Errorf("Skills = %v, want [a z]", got)
	}
	if got := spec.Commands; len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("Commands = %v, want [one two]", got)
	}
}

func TestSelectorMatchesAll(t *testing.T) {
	if SelectorMatchesAll(nil) {
		t.Error("a nil selector must not report matches-all")
	}
	if !SelectorMatchesAll(&metav1.LabelSelector{}) {
		t.Error("an explicitly empty selector must report matches-all")
	}
	if SelectorMatchesAll(&metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}}) {
		t.Error("a populated selector must not report matches-all")
	}
}
