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

package approval

import "testing"

func TestPolicyDecide(t *testing.T) {
	tests := []struct {
		name     string
		ask      []string
		reserved []string
		tool     string
		want     Decision
	}{
		// A deployment that declares nothing keeps running every tool: this gates
		// work that was already permitted, so its absence must change nothing.
		{"no patterns allows everything", nil, nil, "delete_cluster", DecisionAllow},
		{"blank patterns are ignored", []string{"  ", ""}, nil, "delete_cluster", DecisionAllow},

		{"exact name matches", []string{"deploy"}, nil, "deploy", DecisionAsk},
		{"exact name does not match a longer name", []string{"deploy"}, nil, "deploy_cluster", DecisionAllow},
		{"unlisted tool is allowed beside a listed one", []string{"deploy"}, nil, "list_agent", DecisionAllow},

		{"bare star gates everything", []string{"*"}, nil, "list_agent", DecisionAsk},
		{"prefix glob matches", []string{"delete_*"}, nil, "delete_cluster", DecisionAsk},
		{"prefix glob does not match elsewhere", []string{"delete_*"}, nil, "cluster_delete", DecisionAllow},

		// Only a trailing star is a wildcard. A star anywhere else is a literal
		// character, so these patterns can never be malformed and can never
		// match by accident — they simply match nothing.
		{"leading star is literal", []string{"*_cluster"}, nil, "delete_cluster", DecisionAllow},
		{"leading star matches only itself", []string{"*_cluster"}, nil, "*_cluster", DecisionAsk},
		{"interior star is literal", []string{"a*b"}, nil, "aXb", DecisionAllow},
		{"interior star matches only itself", []string{"a*b"}, nil, "a*b", DecisionAsk},

		// Tool names come from an MCP server, so an operator cannot be expected
		// to match its casing.
		{"matching is case-insensitive", []string{"deploy"}, nil, "Deploy", DecisionAsk},
		{"pattern casing does not matter", []string{"DELETE_*"}, nil, "delete_cluster", DecisionAsk},
		{"tool name casing does not matter", []string{"delete_*"}, nil, "DELETE_Cluster", DecisionAsk},

		{"surrounding whitespace is trimmed", []string{" deploy "}, nil, "deploy", DecisionAsk},
		{"tool name whitespace is trimmed", []string{"deploy"}, nil, " deploy ", DecisionAsk},

		// A reserved tool consults the human by contract, so gating it would ask
		// twice for one answer: once to approve asking, once for the answer.
		{"reserved tool is never gated", []string{"*"}, []string{"ask_user_question"}, "ask_user_question", DecisionAllow},
		{"reserved tool is not gated by a prefix", []string{"ask_*"}, []string{"ask_user_question"}, "ask_user_question", DecisionAllow},
		{"reserved match is case-insensitive", []string{"*"}, []string{"ask_user_question"}, "Ask_User_Question", DecisionAllow},
		{"reserving one tool leaves the rest gated", []string{"*"}, []string{"ask_user_question"}, "delete_cluster", DecisionAsk},
		{"blank reserved names are ignored", []string{"*"}, []string{"  "}, "delete_cluster", DecisionAsk},

		// An unnamed call is a framework anomaly: there is no name to match and
		// none to show the approver, so there is nothing to ask about.
		{"empty tool name is allowed", []string{"*"}, nil, "", DecisionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := New(tt.ask, tt.reserved).Decide(tt.tool)
			if got != tt.want {
				t.Fatalf("Decide(%q) with ask=%q reserved=%q = %v, want %v",
					tt.tool, tt.ask, tt.reserved, got, tt.want)
			}
		})
	}
}

func TestPolicyEmpty(t *testing.T) {
	// Empty is what lets a caller skip the approval machinery entirely, so it
	// must track whether anything can actually be gated.
	if !New(nil, nil).Empty() {
		t.Fatal("a policy with no patterns reported itself as non-empty")
	}
	if New([]string{"  "}, nil).Empty() != true {
		t.Fatal("a policy of blank patterns reported itself as non-empty")
	}
	if New([]string{"deploy"}, nil).Empty() {
		t.Fatal("a policy with a pattern reported itself as empty")
	}
	// Reserved names alone gate nothing: reserving is not asking.
	if !New(nil, []string{"ask_user_question"}).Empty() {
		t.Fatal("a policy of reserved names only reported itself as non-empty")
	}
}

func TestPolicyString(t *testing.T) {
	if got := New(nil, nil).String(); got != "none" {
		t.Fatalf("String() = %q, want %q", got, "none")
	}
	if got := New([]string{"Deploy", "delete_*"}, nil).String(); got != "deploy,delete_*" {
		t.Fatalf("String() = %q, want %q", got, "deploy,delete_*")
	}
}

// TestPolicyDeduplicates pins that a pattern reaching the policy twice — the
// normal case, since a run unions its agents', its MCP servers' and the
// server-wide lists — is stored once, so a logged policy stays readable.
func TestPolicyDeduplicates(t *testing.T) {
	p := New([]string{"delete_*", "DELETE_*", " delete_* "}, nil)
	if got := p.String(); got != "delete_*" {
		t.Fatalf("String() = %q, want %q", got, "delete_*")
	}
	if got := p.Decide("delete_cluster"); got != DecisionAsk {
		t.Fatalf("Decide(delete_cluster) = %v, want ask", got)
	}
}

// TestPolicyIsImmutable pins that the constructor copies its inputs: a caller
// that reuses and mutates the slice it passed must not retune a policy that is
// already gating a running turn.
func TestPolicyIsImmutable(t *testing.T) {
	ask := []string{"deploy"}
	p := New(ask, nil)

	ask[0] = "list_agent"

	if got := p.Decide("deploy"); got != DecisionAsk {
		t.Fatalf("Decide(deploy) = %v after the caller mutated its slice, want ask", got)
	}
	if got := p.Decide("list_agent"); got != DecisionAllow {
		t.Fatalf("Decide(list_agent) = %v after the caller mutated its slice, want allow", got)
	}
}
