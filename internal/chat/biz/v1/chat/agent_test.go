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

package chat

import (
	"testing"

	chat "github.com/onexstack/onexai/pkg/known/chat"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

func TestAgentFromConvID(t *testing.T) {
	cases := []struct {
		convID   string
		wantName string
		wantOK   bool
	}{
		{"robot:foo", "foo", true},
		{"platform:bke-agent", "bke-agent", true},
		{"robot:", "", true},
		{"oc_123", "", false},
		{"", "", false},
		{"robot", "", false},
	}
	for _, tc := range cases {
		got, ok := agentFromConvID(tc.convID)
		if ok != tc.wantOK || got != tc.wantName {
			t.Errorf("agentFromConvID(%q) = (%q, %v), want (%q, %v)", tc.convID, got, ok, tc.wantName, tc.wantOK)
		}
	}
}

func TestIsVirtualConv(t *testing.T) {
	cases := map[string]bool{
		"robot:foo":       true,
		"platform:bke":    true,
		"oc_123":          false,
		"":                false,
		"robot:foo:extra": true,
	}
	for convID, want := range cases {
		if got := isVirtualConv(convID); got != want {
			t.Errorf("isVirtualConv(%q) = %v, want %v", convID, got, want)
		}
	}
}

func TestSortAgents(t *testing.T) {
	agents := []*v1.Agent{
		{Name: "bke-agent"},
		{Name: "__builtin__"},
		{Name: "sre-agent"},
		// The built-in, by the constant rather than by a literal: the name gained
		// a namespace, and a test that spelled it out would have kept passing
		// against the old one.
		{Name: chat.DefaultAgentName},
	}
	sortAgents(agents)
	// Built-in agents must come first; the rest keep their relative order.
	if !isBuiltinAgent(agents[0].GetName()) || !isBuiltinAgent(agents[1].GetName()) {
		t.Fatalf("built-in agents not ordered first: %v", agentNames(agents))
	}
	if agents[2].GetName() != "bke-agent" || agents[3].GetName() != "sre-agent" {
		t.Fatalf("non-builtin order changed: %v", agentNames(agents))
	}
}

func agentNames(agents []*v1.Agent) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.GetName())
	}
	return out
}
