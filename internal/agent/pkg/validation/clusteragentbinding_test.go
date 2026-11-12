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

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func TestValidateClusterBindingNamespacesAcceptsTheWildcard(t *testing.T) {
	if err := validateClusterBindingNamespaces([]string{agent.AllNamespaces}); err != nil {
		t.Fatalf("wildcard must be valid, got %v", err)
	}
}

func TestValidateClusterBindingNamespacesRefusesMixedWildcard(t *testing.T) {
	for _, namespaces := range [][]string{
		{agent.AllNamespaces, "team-a"},
		{"team-a", agent.AllNamespaces},
	} {
		if err := validateClusterBindingNamespaces(namespaces); err == nil {
			t.Fatalf("namespaces %v: want an error", namespaces)
		}
	}
}

func TestValidateClusterBindingNamespacesKeepsTheExplicitRules(t *testing.T) {
	if err := validateClusterBindingNamespaces(nil); err == nil {
		t.Error("an empty list must stay invalid")
	}
	if err := validateClusterBindingNamespaces([]string{""}); err == nil {
		t.Error("an empty namespace must stay invalid")
	}
	if err := validateClusterBindingNamespaces([]string{"Team_A"}); err == nil {
		t.Error("an invalid DNS-1123 label must stay invalid")
	}
}
