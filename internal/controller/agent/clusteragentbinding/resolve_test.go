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

package clusteragentbinding

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// stubClient answers the two reads these functions make. The controller-runtime
// fake client is not usable here: its structured-merge-diff dependency is a
// different major version than the one this module pins.
type stubClient struct {
	client.Client
	skills     map[types.NamespacedName]*agentv1alpha1.Skill
	agents     []*agentv1alpha1.Agent
	namespaces []*corev1.Namespace
}

func (s *stubClient) Get(
	_ context.Context, key types.NamespacedName, obj client.Object, _ ...client.GetOption,
) error {
	if target, ok := obj.(*agentv1alpha1.Skill); ok {
		if skill, found := s.skills[key]; found {
			*target = *skill.DeepCopy()
			return nil
		}
		return apierrors.NewNotFound(schema.GroupResource{Group: agentv1alpha1.GroupName, Resource: "skills"}, key.Name)
	}
	return apierrors.NewNotFound(schema.GroupResource{Group: agentv1alpha1.GroupName}, key.Name)
}

func (s *stubClient) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	switch target := list.(type) {
	case *agentv1alpha1.AgentList:
		for _, agent := range s.agents {
			target.Items = append(target.Items, *agent.DeepCopy())
		}
	case *corev1.NamespaceList:
		for _, namespace := range s.namespaces {
			target.Items = append(target.Items, *namespace.DeepCopy())
		}
	}
	return nil
}

func TestMatchesAgentUsesTheNamespaceListAsTheBoundary(t *testing.T) {
	binding := &agentv1alpha1.ClusterAgentBinding{
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Namespaces:    []string{"team-a"},
			AgentSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"platform": "true"}},
		},
	}

	inside := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: "helper", Namespace: "team-a", Labels: map[string]string{"platform": "true"},
	}}
	outside := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: "helper", Namespace: "team-b", Labels: map[string]string{"platform": "true"},
	}}
	unlabelled := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: "other", Namespace: "team-a",
	}}

	assert.True(t, MatchesAgent(binding, inside))
	// A matching label in a namespace the binding never named must not pull the
	// agent in: the namespace list is the grant.
	assert.False(t, MatchesAgent(binding, outside))
	assert.False(t, MatchesAgent(binding, unlabelled))
}

func TestMatchesAgentHonoursAnExplicitName(t *testing.T) {
	binding := &agentv1alpha1.ClusterAgentBinding{
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Namespaces: []string{"team-a"},
			Agents:     []agentv1alpha1.Reference{{Namespace: "team-a", Name: "helper"}},
		},
	}
	agent := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper", Namespace: "team-a"}}
	assert.True(t, MatchesAgent(binding, agent))

	other := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper", Namespace: "team-b"}}
	assert.False(t, MatchesAgent(binding, other))
}

func TestMatchesAgentSkipsADisabledBinding(t *testing.T) {
	disabled := false
	binding := &agentv1alpha1.ClusterAgentBinding{
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Enabled:    &disabled,
			Namespaces: []string{"team-a"},
		},
	}
	agent := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper", Namespace: "team-a"}}
	assert.False(t, MatchesAgent(binding, agent))
}

func TestComputeResolvesCrossNamespaceReferences(t *testing.T) {
	key := types.NamespacedName{Namespace: "platform", Name: "k8s-debug"}
	c := &stubClient{skills: map[types.NamespacedName]*agentv1alpha1.Skill{
		key: {
			ObjectMeta: metav1.ObjectMeta{Name: "k8s-debug", Namespace: "platform"},
			Spec:       agentv1alpha1.SkillSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}},
		},
	}}
	binding := &agentv1alpha1.ClusterAgentBinding{
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Namespaces: []string{"team-a"},
			Skills: []agentv1alpha1.Reference{
				{Name: "k8s-debug", Namespace: "platform"},
				{Name: "gone", Namespace: "platform"},
			},
			// A cluster-scoped object has no namespace to default to.
			Prompts: []agentv1alpha1.Reference{{Name: "base"}},
		},
	}

	got, err := Compute(context.Background(), c, binding)
	require.NoError(t, err)

	assert.Equal(t, []string{"platform/k8s-debug"}, got.Skills)
	assert.Equal(t, []string{"Prompt/base (namespace missing)", "Skill/platform/gone"}, got.Missing)
}

func TestComputeDropsADisabledTarget(t *testing.T) {
	key := types.NamespacedName{Namespace: "platform", Name: "k8s-debug"}
	c := &stubClient{skills: map[types.NamespacedName]*agentv1alpha1.Skill{
		key: {ObjectMeta: metav1.ObjectMeta{Name: "k8s-debug", Namespace: "platform"}},
	}}
	binding := &agentv1alpha1.ClusterAgentBinding{
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Namespaces: []string{"team-a"},
			Skills:     []agentv1alpha1.Reference{{Name: "k8s-debug", Namespace: "platform"}},
		},
	}

	got, err := Compute(context.Background(), c, binding)
	require.NoError(t, err)

	assert.Empty(t, got.Skills)
	assert.Equal(t, []string{"Skill/platform/k8s-debug"}, got.Missing)
}

func TestMatchAgentsCountsAcrossNamespaces(t *testing.T) {
	binding := &agentv1alpha1.ClusterAgentBinding{
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Namespaces:    []string{"team-a", "team-b"},
			AgentSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"platform": "true"}},
		},
	}
	c := &stubClient{agents: []*agentv1alpha1.Agent{
		{ObjectMeta: metav1.ObjectMeta{
			Name: "a", Namespace: "team-a", Labels: map[string]string{"platform": "true"}},
			Spec: agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		{ObjectMeta: metav1.ObjectMeta{
			Name: "b", Namespace: "team-b", Labels: map[string]string{"platform": "true"}},
			Spec: agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		// Not selected: wrong namespace.
		{ObjectMeta: metav1.ObjectMeta{
			Name: "c", Namespace: "team-c", Labels: map[string]string{"platform": "true"}},
			Spec: agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		// Not selected: wrong label.
		{ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "team-a"},
			Spec: agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
	}}

	matched, err := MatchAgents(context.Background(), c, binding)
	require.NoError(t, err)
	assert.Equal(t, int32(2), matched)
}

func TestMatchesAgentWildcardCoversEveryNamespace(t *testing.T) {
	binding := &agentv1alpha1.ClusterAgentBinding{
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Namespaces:    []string{agentv1alpha1.AllNamespaces},
			AgentSelector: &metav1.LabelSelector{},
		},
	}

	for _, namespace := range []string{"team-a", "default", "kube-system"} {
		agent := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper", Namespace: namespace}}
		assert.True(t, MatchesAgent(binding, agent), "namespace %q must be covered by the wildcard", namespace)
	}

	disabled := false
	binding.Spec.Enabled = &disabled
	agent := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper", Namespace: "team-a"}}
	assert.False(t, MatchesAgent(binding, agent), "a disabled wildcard binding selects nothing")
}

func TestMatchAgentsWildcardCountsEveryAgent(t *testing.T) {
	binding := &agentv1alpha1.ClusterAgentBinding{
		Spec: agentv1alpha1.ClusterAgentBindingSpec{
			Namespaces:    []string{agentv1alpha1.AllNamespaces},
			AgentSelector: &metav1.LabelSelector{},
		},
	}
	c := &stubClient{agents: []*agentv1alpha1.Agent{
		{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "team-a"},
			Spec: agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"},
			Spec: agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "kube-system"},
			Spec: agentv1alpha1.AgentSpec{CommonSpec: agentv1alpha1.CommonSpec{Enabled: true}}},
		// Disabled agents never count.
		{ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "team-a"}},
	}}

	matched, err := MatchAgents(context.Background(), c, binding)
	require.NoError(t, err)
	assert.Equal(t, int32(3), matched)
}

func TestCountNamespacesWildcardCountsEveryNamespace(t *testing.T) {
	c := &stubClient{namespaces: []*corev1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
	}}
	r := &Reconciler{Client: c}

	present, err := r.countNamespaces(context.Background(), []string{agentv1alpha1.AllNamespaces})
	require.NoError(t, err)
	assert.Equal(t, int32(2), present)
}
