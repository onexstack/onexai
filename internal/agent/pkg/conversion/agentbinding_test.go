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
	"time"

	"github.com/stretchr/testify/assert"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func TestAgentBindingCRToV1(t *testing.T) {
	disabled := false
	cr := &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "base",
			Namespace:         "default",
			Labels:            map[string]string{"team": "sre"},
			CreationTimestamp: metav1.NewTime(time.UnixMilli(1700000000000)),
		},
		Spec: agentv1alpha1.AgentBindingSpec{
			Enabled:     &disabled,
			DisplayName: "Base binding",
			Description: "contributes the base prompt",
			Owner: &rbacv1.Subject{
				Kind:     "User",
				Name:     "alice",
				APIGroup: "rbac.authorization.k8s.io",
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"platform": "true"},
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "tier",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"gold"},
				}},
			},
			Agents:         []string{"helper"},
			Prompts:        []agentv1alpha1.Reference{{Name: "base-prompt"}},
			Skills:         []agentv1alpha1.Reference{{Name: "k8s-debug"}},
			KnowledgeBases: []agentv1alpha1.Reference{{Name: "kb-runbook"}},
			MCPServers:     []agentv1alpha1.Reference{{Name: "gitlab"}},
			CustomCommands: []agentv1alpha1.Reference{{Name: "triage"}},
			Commands:       []string{"bind", "oncall"},
		},
		Status: agentv1alpha1.AgentBindingStatus{
			CommonStatus:  agentv1alpha1.CommonStatus{ObservedGeneration: 4},
			MatchedAgents: 3,
			Resolved: agentv1alpha1.AgentBindingResolved{
				Prompts:        []string{"base-prompt"},
				Skills:         []string{"k8s-debug"},
				MCPs:           []string{"gitlab"},
				KnowledgeBases: []string{"kb-runbook"},
				CustomCommands: []string{"triage"},
				Commands:       []string{"bind", "oncall"},
			},
			Missing: []string{"Skill/default/ghost"},
		},
	}

	out := AgentBindingCRToV1(cr)
	assert.Equal(t, "base", out.GetName())
	assert.Equal(t, "default", out.GetNamespace())
	assert.Equal(t, "sre", out.GetLabels()["team"])
	assert.Equal(t, int64(1700000000000), out.GetCreatedAt())
	assert.Equal(t, int64(1700000000000), out.GetUpdatedAt())

	spec := out.GetSpec()
	requireBoolPtr(t, spec.Enabled, false)
	assert.Equal(t, "Base binding", spec.GetDisplayName())
	assert.Equal(t, "alice", spec.GetOwner().GetName())
	assert.Equal(t, "gold", spec.GetSelector().GetMatchExpressions()[0].GetValues()[0])
	assert.Equal(t, []string{"helper"}, spec.GetAgents())
	assert.Equal(t, "base-prompt", spec.GetPrompts()[0].GetName())
	assert.Equal(t, "gitlab", spec.GetMcpservers()[0].GetName())
	assert.Equal(t, []string{"bind", "oncall"}, spec.GetCommands())

	got := out.GetStatus()
	assert.Equal(t, int64(4), got.GetObservedGeneration())
	assert.Equal(t, int32(3), got.GetMatchedAgents())
	assert.Equal(t, []string{"kb-runbook"}, got.GetResolved().GetKnowledgeBases())
	assert.Equal(t, []string{"Skill/default/ghost"}, got.GetMissing())
}

func TestAgentBindingV1ToCRRoundTrip(t *testing.T) {
	disabled := false
	in := &v1.AgentBinding{
		Name:      "base",
		Namespace: "team-a",
		Labels:    map[string]string{"team": "sre"},
		Spec: &v1.AgentBindingSpec{
			Enabled:     &disabled,
			DisplayName: "Base binding",
			Description: "contributes the base prompt",
			Owner: &v1.AgentBindingSubject{
				Kind:     "User",
				Name:     "alice",
				ApiGroup: "rbac.authorization.k8s.io",
			},
			Selector: &v1.AgentBindingLabelSelector{
				MatchLabels: map[string]string{"platform": "true"},
				MatchExpressions: []*v1.AgentBindingLabelSelectorRequirement{{
					Key:      "tier",
					Operator: string(metav1.LabelSelectorOpIn),
					Values:   []string{"gold"},
				}},
			},
			Agents:         []string{"helper"},
			Prompts:        []*v1.ObjectReference{{Name: "base-prompt"}},
			Skills:         []*v1.ObjectReference{{Name: "k8s-debug"}},
			KnowledgeBases: []*v1.ObjectReference{{Name: "kb-runbook"}},
			Mcpservers:     []*v1.ObjectReference{{Name: "gitlab"}},
			CustomCommands: []*v1.ObjectReference{{Name: "triage"}},
			Commands:       []string{"bind", "oncall"},
		},
		// Status is read-only: a caller that echoed it back must not be able to
		// overwrite what the controller recorded.
		Status: &v1.AgentBindingStatus{MatchedAgents: 99},
	}

	cr := AgentBindingV1ToCR(in)
	assert.Equal(t, "base", cr.Name)
	assert.Equal(t, "team-a", cr.Namespace)
	assert.Equal(t, "sre", cr.Labels["team"])
	assert.Equal(t, in.Spec.Enabled, cr.Spec.Enabled)
	assert.Equal(t, in.Spec.DisplayName, cr.Spec.DisplayName)
	assert.Equal(t, in.Spec.Description, cr.Spec.Description)
	assert.Equal(t, "alice", cr.Spec.Owner.Name)
	assert.Equal(t, "rbac.authorization.k8s.io", cr.Spec.Owner.APIGroup)
	assert.Equal(t, in.Spec.Selector.MatchLabels, cr.Spec.Selector.MatchLabels)
	assert.Equal(t, metav1.LabelSelectorOpIn, cr.Spec.Selector.MatchExpressions[0].Operator)
	assert.Equal(t, []string{"helper"}, cr.Spec.Agents)
	assert.Equal(t, []agentv1alpha1.Reference{{Name: "base-prompt"}}, cr.Spec.Prompts)
	assert.Equal(t, []agentv1alpha1.Reference{{Name: "k8s-debug"}}, cr.Spec.Skills)
	assert.Equal(t, []agentv1alpha1.Reference{{Name: "kb-runbook"}}, cr.Spec.KnowledgeBases)
	assert.Equal(t, []agentv1alpha1.Reference{{Name: "gitlab"}}, cr.Spec.MCPServers)
	assert.Equal(t, []agentv1alpha1.Reference{{Name: "triage"}}, cr.Spec.CustomCommands)
	assert.Equal(t, []string{"bind", "oncall"}, cr.Spec.Commands)

	// Status never crosses the write path.
	assert.Equal(t, agentv1alpha1.AgentBindingStatus{}, cr.Status)

	// The round trip preserves the writable spec.
	back := AgentBindingCRToV1(cr)
	assert.Equal(t, in.GetSpec().GetEnabled(), back.GetSpec().GetEnabled())
	assert.Equal(t, in.GetSpec().GetSelector(), back.GetSpec().GetSelector())
	assert.Equal(t, in.GetSpec().GetPrompts(), back.GetSpec().GetPrompts())
}

func TestAgentBindingAbsentEnabledStaysAbsent(t *testing.T) {
	in := &v1.AgentBinding{Name: "base", Namespace: "default"}
	cr := AgentBindingV1ToCR(in)
	assert.Nil(t, cr.Spec.Enabled)
	// The pointer must survive the return trip: the getter would collapse an
	// absent value to false, which is exactly the state this test guards.
	assert.Nil(t, AgentBindingCRToV1(cr).GetSpec().Enabled)
}

func TestAgentBindingNilInputs(t *testing.T) {
	assert.Nil(t, AgentBindingCRToV1(nil))
	cr := AgentBindingV1ToCR(nil)
	assert.NotNil(t, cr)
	assert.Empty(t, cr.Name)
}

func requireBoolPtr(t *testing.T, got *bool, want bool) {
	t.Helper()
	if got == nil {
		t.Fatalf("pointer = nil, want %v", want)
	}
	assert.Equal(t, want, *got)
}
