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

package agent

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func agentIn(namespace, name string) *agentv1alpha1.Agent {
	return &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
	}
}

func TestAgentsForClusterBindingWildcardEnqueuesEveryAgent(t *testing.T) {
	c := &memoryClient{objects: []client.Object{
		agentIn("team-a", "a"),
		agentIn("default", "b"),
		agentIn("kube-system", "c"),
	}}
	r := &Reconciler{Client: c}
	binding := &agentv1alpha1.ClusterAgentBinding{
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Namespaces: []string{agentv1alpha1.AllNamespaces},
		},
	}

	requests := r.agentsForClusterBinding(context.Background(), binding)
	if len(requests) != 3 {
		t.Fatalf("got %d requests, want 3: %+v", len(requests), requests)
	}
	got := make(map[types.NamespacedName]struct{}, len(requests))
	for _, request := range requests {
		got[request.NamespacedName] = struct{}{}
	}
	for _, key := range []types.NamespacedName{
		{Namespace: "team-a", Name: "a"},
		{Namespace: "default", Name: "b"},
		{Namespace: "kube-system", Name: "c"},
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing request for %s", key)
		}
	}
}

func TestAgentsForClusterBindingExplicitNamespacesOnly(t *testing.T) {
	c := &memoryClient{objects: []client.Object{
		agentIn("team-a", "a"),
		agentIn("team-b", "b"),
		agentIn("team-c", "c"),
	}}
	r := &Reconciler{Client: c}
	binding := &agentv1alpha1.ClusterAgentBinding{
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Namespaces: []string{"team-a", "team-b", "team-a"},
		},
	}

	requests := r.agentsForClusterBinding(context.Background(), binding)
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2: %+v", len(requests), requests)
	}
	for _, request := range requests {
		if request.Namespace == "team-c" {
			t.Errorf("agent in an unlisted namespace was enqueued: %v", request)
		}
	}
}

func TestAgentsForClusterBindingIgnoresOtherTypes(t *testing.T) {
	r := &Reconciler{Client: &memoryClient{}}
	if requests := r.agentsForClusterBinding(context.Background(), &agentv1alpha1.Agent{}); requests != nil {
		t.Errorf("requests = %+v, want nil for a non-binding object", requests)
	}
}
