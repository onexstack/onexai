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

package conversion

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// Every reference must survive the wire round trip with its namespace; that is
// the whole point of the cluster-scoped shape.
func TestClusterAgentBindingRoundTripKeepsNamespaces(t *testing.T) {
	enabled := true
	in := &v1.ClusterAgentBinding{
		Name:   "platform-base",
		Labels: map[string]string{"tier": "platform"},
		Spec: &v1.ClusterAgentBindingSpec{
			Enabled:     &enabled,
			DisplayName: "平台基础能力",
			Namespaces:  []string{"team-a", "team-b"},
			Agents: []*v1.ObjectReference{
				{Name: "helper", Namespace: ptrString("team-c")},
			},
			Skills: []*v1.ObjectReference{
				{Name: "k8s-debug", Namespace: ptrString("platform")},
			},
			MemoryRef: &v1.ObjectReference{Name: "team-memory", Namespace: ptrString("platform")},
		},
	}

	cr := ClusterAgentBindingV1ToCR(in)
	require.Equal(t, []string{"team-a", "team-b"}, cr.Spec.Namespaces)
	require.Equal(t, []agentv1alpha1.Reference{{Name: "helper", Namespace: "team-c"}}, cr.Spec.Agents)
	require.Equal(t, &agentv1alpha1.Reference{Name: "team-memory", Namespace: "platform"}, cr.Spec.MemoryRef)

	cr.Status.MatchedNamespaces = 2
	cr.Status.MatchedAgents = 3
	cr.Status.Resolved.Skills = []string{"platform/k8s-debug"}
	cr.Status.Missing = []string{"Skill/platform/gone"}

	out := ClusterAgentBindingCRToV1(cr)
	require.Equal(t, "platform-base", out.GetName())
	require.Equal(t, []string{"team-a", "team-b"}, out.GetSpec().GetNamespaces())
	require.Equal(t, "platform/k8s-debug", out.GetStatus().GetResolved().GetSkills()[0])
	require.Equal(t, int32(3), out.GetStatus().GetMatchedAgents())
	require.Equal(t, "team-c", out.GetSpec().GetAgents()[0].GetNamespace())
}

// A request that echoes status back must not overwrite what the controller
// observed: the wire -> CR direction never reads status.
func TestClusterAgentBindingV1ToCRDropsStatus(t *testing.T) {
	in := &v1.ClusterAgentBinding{
		Name: "platform-base",
		Spec: &v1.ClusterAgentBindingSpec{Namespaces: []string{"team-a"}},
		Status: &v1.ClusterAgentBindingStatus{
			MatchedAgents: 99,
			Missing:       []string{"Skill/platform/gone"},
		},
	}

	cr := ClusterAgentBindingV1ToCR(in)
	assert.Zero(t, cr.Status.MatchedAgents)
	assert.Empty(t, cr.Status.Missing)
	assert.Empty(t, cr.Namespace, "a cluster-scoped object has no namespace")
}

func ptrString(value string) *string { return &value }
