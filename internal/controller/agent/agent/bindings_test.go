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

package agent

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/event"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func TestBindingMatches(t *testing.T) {
	agent := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name:   "helper",
		Labels: map[string]string{"platform": "true", "team": "sre"},
	}}
	agentLabels := labels.Set(agent.Labels)

	cases := []struct {
		name    string
		binding *agentv1alpha1.AgentBinding
		want    bool
	}{
		{
			name:    "selector matches",
			binding: &agentv1alpha1.AgentBinding{Spec: agentv1alpha1.AgentBindingSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"platform": "true"}}}},
			want:    true,
		},
		{
			name:    "empty selector selects everything",
			binding: &agentv1alpha1.AgentBinding{Spec: agentv1alpha1.AgentBindingSpec{Selector: &metav1.LabelSelector{}}},
			want:    true,
		},
		{
			name:    "named even without a selector match",
			binding: &agentv1alpha1.AgentBinding{Spec: agentv1alpha1.AgentBindingSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"platform": "false"}}, Agents: []string{"helper"}}},
			want:    true,
		},
		{
			name:    "selector does not match",
			binding: &agentv1alpha1.AgentBinding{Spec: agentv1alpha1.AgentBindingSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"platform": "false"}}}},
			want:    false,
		},
		{
			name:    "nil selector matches nothing",
			binding: &agentv1alpha1.AgentBinding{Spec: agentv1alpha1.AgentBindingSpec{}},
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bindingMatches(tc.binding, agent, agentLabels); got != tc.want {
				t.Errorf("bindingMatches() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLocalizeCustomCommands(t *testing.T) {
	cases := []struct {
		name      string
		namespace string
		entries   []string
		want      []string
		wantMiss  []string
	}{
		{
			name:      "same namespace is reduced to the bare name",
			namespace: "helper-public",
			entries:   []string{"helper-public/hello", "hello"},
			want:      []string{"hello", "hello"},
		},
		{
			name:      "cross namespace is reported instead of kept",
			namespace: "default",
			entries:   []string{"helper-public/hello"},
			want:      []string{},
			wantMiss:  []string{"CustomCommand/helper-public/hello (cross-namespace)"},
		},
		{
			name:      "empty entries stay empty",
			namespace: "default",
			entries:   nil,
			want:      nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var missing []string
			got := localizeCustomCommands(tc.namespace, tc.entries, &missing)
			if len(got) != len(tc.want) {
				t.Fatalf("localizeCustomCommands() = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("localizeCustomCommands() = %v, want %v", got, tc.want)
				}
			}
			if len(missing) != len(tc.wantMiss) {
				t.Fatalf("missing = %v, want %v", missing, tc.wantMiss)
			}
			for i := range tc.wantMiss {
				if missing[i] != tc.wantMiss[i] {
					t.Fatalf("missing = %v, want %v", missing, tc.wantMiss)
				}
			}
		})
	}
}

func TestSortedUnique(t *testing.T) {
	got := sortedUnique([]string{"b", "a", "b", "c", "a"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("sortedUnique() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedUnique() = %v, want %v", got, want)
		}
	}
}

func TestBindingsCondition(t *testing.T) {
	agent := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper"}, Spec: agentv1alpha1.AgentSpec{Enabled: true}}
	agent.Generation = 4

	cases := []struct {
		name       string
		set        bindingSet
		wantReason string
		wantStatus metav1.ConditionStatus
	}{
		{
			name:       "no bindings",
			set:        bindingSet{},
			wantReason: "NoBindings",
			wantStatus: metav1.ConditionTrue,
		},
		{
			name:       "merged",
			set:        bindingSet{total: 2},
			wantReason: "Merged",
			wantStatus: metav1.ConditionTrue,
		},
		{
			name:       "stale status",
			set:        bindingSet{total: 1, stale: true},
			wantReason: "BindingStale",
			wantStatus: metav1.ConditionTrue,
		},
		{
			name:       "missing reference",
			set:        bindingSet{total: 1, missing: []string{"Skill/ns/gone"}},
			wantReason: "ReferenceNotFound",
			wantStatus: metav1.ConditionFalse,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			condition := bindingsCondition(agent, tc.set)
			if condition.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", condition.Reason, tc.wantReason)
			}
			if condition.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", condition.Status, tc.wantStatus)
			}
		})
	}
}

func TestBindingChangePredicateWakesOnStatusOnlyChanges(t *testing.T) {
	base := func() *agentv1alpha1.AgentBinding {
		return &agentv1alpha1.AgentBinding{
			ObjectMeta: metav1.ObjectMeta{Generation: 3, Labels: map[string]string{"team": "sre"}},
			Status: agentv1alpha1.AgentBindingStatus{
				ObservedGeneration: 3,
				Resolved: agentv1alpha1.AgentBindingResolved{
					CustomCommands: []string{"alpha"},
				},
			},
		}
	}

	cases := []struct {
		name   string
		mutate func(*agentv1alpha1.AgentBinding)
		want   bool
	}{
		{name: "no change", mutate: func(*agentv1alpha1.AgentBinding) {}, want: false},
		{name: "generation", mutate: func(b *agentv1alpha1.AgentBinding) { b.Generation = 4 }, want: true},
		{name: "labels", mutate: func(b *agentv1alpha1.AgentBinding) {
			b.Labels = map[string]string{"team": "platform"}
		}, want: true},
		{name: "resolved custom commands", mutate: func(b *agentv1alpha1.AgentBinding) {
			b.Status.Resolved.CustomCommands = []string{"alpha", "beta"}
		}, want: true},
		{name: "observed generation", mutate: func(b *agentv1alpha1.AgentBinding) {
			b.Status.ObservedGeneration = 4
		}, want: true},
		{name: "missing refs", mutate: func(b *agentv1alpha1.AgentBinding) {
			b.Status.Missing = []string{"CustomCommand/gone"}
		}, want: true},
	}

	pred := bindingChangePredicate()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldBinding := base()
			newBinding := base()
			tc.mutate(newBinding)
			got := pred.Update(event.UpdateEvent{ObjectOld: oldBinding, ObjectNew: newBinding})
			if got != tc.want {
				t.Errorf("predicate = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestClusterBindingChangePredicateWakesOnStatusOnlyChanges(t *testing.T) {
	base := func() *agentv1alpha1.ClusterAgentBinding {
		return &agentv1alpha1.ClusterAgentBinding{
			ObjectMeta: metav1.ObjectMeta{Generation: 2, Labels: map[string]string{"team": "sre"}},
			Status: agentv1alpha1.ClusterAgentBindingStatus{
				CommonStatus: agentv1alpha1.CommonStatus{ObservedGeneration: 2},
				Resolved: agentv1alpha1.AgentBindingResolved{
					CustomCommands: []string{"alpha"},
				},
			},
		}
	}

	cases := []struct {
		name   string
		mutate func(*agentv1alpha1.ClusterAgentBinding)
		want   bool
	}{
		{name: "no change", mutate: func(*agentv1alpha1.ClusterAgentBinding) {}, want: false},
		{name: "generation", mutate: func(b *agentv1alpha1.ClusterAgentBinding) { b.Generation = 3 }, want: true},
		{name: "resolved custom commands", mutate: func(b *agentv1alpha1.ClusterAgentBinding) {
			b.Status.Resolved.CustomCommands = []string{"alpha", "beta"}
		}, want: true},
		{name: "observed generation", mutate: func(b *agentv1alpha1.ClusterAgentBinding) {
			b.Status.ObservedGeneration = 3
		}, want: true},
	}

	pred := clusterBindingChangePredicate()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldBinding := base()
			newBinding := base()
			tc.mutate(newBinding)
			got := pred.Update(event.UpdateEvent{ObjectOld: oldBinding, ObjectNew: newBinding})
			if got != tc.want {
				t.Errorf("predicate = %t, want %t", got, tc.want)
			}
		})
	}
}
