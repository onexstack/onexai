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

package di

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func TestAgentResolvedChanged(t *testing.T) {
	base := func() *agentv1alpha1.Agent {
		return &agentv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "helper"},
			Status: agentv1alpha1.AgentStatus{
				Resolved: agentv1alpha1.AgentResolved{
					MCPs:           []string{"oai-chat-apiserver"},
					CustomCommands: []string{"alpha"},
				},
			},
		}
	}

	cases := []struct {
		name   string
		mutate func(*agentv1alpha1.Agent)
		want   bool
	}{
		{name: "no change", mutate: func(*agentv1alpha1.Agent) {}, want: false},
		{name: "custom commands", mutate: func(a *agentv1alpha1.Agent) {
			a.Status.Resolved.CustomCommands = []string{"alpha", "beta"}
		}, want: true},
		{name: "mcps", mutate: func(a *agentv1alpha1.Agent) {
			a.Status.Resolved.MCPs = nil
		}, want: true},
		{
			name: "conditions only",
			mutate: func(a *agentv1alpha1.Agent) {
				a.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldAgent := base()
			newAgent := base()
			tc.mutate(newAgent)
			if got := agentResolvedChanged(oldAgent, newAgent); got != tc.want {
				t.Errorf("agentResolvedChanged = %t, want %t", got, tc.want)
			}
		})
	}
}
