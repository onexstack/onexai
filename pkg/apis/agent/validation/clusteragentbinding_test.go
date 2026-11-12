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

package validation

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func clusterBindingWithNamespaces(namespaces ...string) *agent.ClusterAgentBindingSpec {
	return &agent.ClusterAgentBindingSpec{
		Namespaces:    namespaces,
		AgentSelector: &metav1.LabelSelector{},
	}
}

func TestValidateClusterAgentBindingAcceptsTheWildcard(t *testing.T) {
	spec := clusterBindingWithNamespaces(agent.AllNamespaces)
	if errs := ValidateClusterAgentBinding(spec, field.NewPath("spec")); len(errs) != 0 {
		t.Fatalf("wildcard must be valid, got %v", errs)
	}
}

func TestValidateClusterAgentBindingRefusesWildcardMixedWithNames(t *testing.T) {
	for _, namespaces := range [][]string{
		{agent.AllNamespaces, "team-a"},
		{"team-a", agent.AllNamespaces},
	} {
		spec := clusterBindingWithNamespaces(namespaces...)
		errs := ValidateClusterAgentBinding(spec, field.NewPath("spec"))
		if len(errs) != 1 {
			t.Fatalf("namespaces %v: want exactly one error, got %v", namespaces, errs)
		}
		if errs[0].Field != "spec.namespaces" {
			t.Errorf("namespaces %v: error on %q, want spec.namespaces", namespaces, errs[0].Field)
		}
	}
}

func TestValidateClusterAgentBindingStillRefusesAnEmptyList(t *testing.T) {
	spec := clusterBindingWithNamespaces()
	errs := ValidateClusterAgentBinding(spec, field.NewPath("spec"))
	if len(errs) == 0 {
		t.Fatal("an empty namespace list must stay invalid")
	}
}

func TestNamespaceInListMatchesTheWildcard(t *testing.T) {
	if !NamespaceInList([]string{agent.AllNamespaces}, "team-a") {
		t.Error("the wildcard must match every namespace")
	}
	if !NamespaceInList([]string{agent.AllNamespaces}, "default") {
		t.Error("the wildcard must match system namespaces too")
	}
	if NamespaceInList([]string{"team-a"}, "team-b") {
		t.Error("an explicit list must not match a namespace it does not name")
	}
	if !NamespaceInList([]string{"team-a"}, "team-a") {
		t.Error("an explicit list must match the namespace it names")
	}
}

func TestIsAllNamespacesRequiresTheSingleEntry(t *testing.T) {
	if !IsAllNamespaces([]string{agent.AllNamespaces}) {
		t.Error("a single wildcard entry is the wildcard spelling")
	}
	if IsAllNamespaces([]string{agent.AllNamespaces, "team-a"}) {
		t.Error("a mixed list is not the wildcard spelling")
	}
	if IsAllNamespaces(nil) {
		t.Error("an empty list is not the wildcard spelling")
	}
}
