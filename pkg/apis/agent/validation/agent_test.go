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

package validation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func TestValidateAgentAggregates(t *testing.T) {
	valid := func() *agent.AgentSpec {
		return &agent.AgentSpec{
			CommonSpec: agent.CommonSpec{Enabled: true},
			Agents:     []agent.Reference{{Name: "base-sre"}, {Name: "base-k8s"}},
			AgentSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"tier": "base"},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*agent.AgentSpec)
		wantErr string
	}{
		{name: "names and selector are a union", mutate: func(*agent.AgentSpec) {}},
		{
			name:    "a reference must name an agent",
			mutate:  func(s *agent.AgentSpec) { s.Agents = []agent.Reference{{}} },
			wantErr: "spec.agents[0].name",
		},
		{
			name:    "a delegate must name an agent",
			mutate:  func(s *agent.AgentSpec) { s.Delegates = []agent.Reference{{}} },
			wantErr: "spec.delegates[0].name",
		},
		{
			name: "cross-namespace delegation is rejected",
			mutate: func(s *agent.AgentSpec) {
				s.Delegates = []agent.Reference{{Name: "researcher", Namespace: "other"}}
			},
			wantErr: "spec.delegates[0].namespace",
		},
		{
			name: "cross-namespace aggregation is rejected",
			mutate: func(s *agent.AgentSpec) {
				s.Agents = []agent.Reference{{Name: "base-sre", Namespace: "other"}}
			},
			wantErr: "spec.agents[0].namespace",
		},
		{
			name: "too many explicit sources",
			mutate: func(s *agent.AgentSpec) {
				s.Agents = nil
				for i := 0; i <= maxAgentRefs; i++ {
					s.Agents = append(s.Agents, agent.Reference{Name: fmt.Sprintf("a%d", i)})
				}
			},
			wantErr: "spec.agents",
		},
		{
			name: "an unparseable selector is rejected",
			mutate: func(s *agent.AgentSpec) {
				s.AgentSelector = &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "tier", Operator: "Bogus"},
					},
				}
			},
			wantErr: "spec.agentSelector",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid()
			tc.mutate(spec)
			errs := ValidateAgent(spec, field.NewPath("spec"))
			if tc.wantErr == "" {
				assert.Empty(t, errs)
				return
			}
			assert.True(t, containsField(errs, tc.wantErr), "want an error on %s, got %v", tc.wantErr, errs)
		})
	}
}

func containsField(errs field.ErrorList, path string) bool {
	for _, err := range errs {
		if err.Field == path || strings.HasPrefix(err.Field, path+"[") || strings.HasPrefix(err.Field, path+".") {
			return true
		}
	}
	return false
}

// An external agent's behaviour belongs to its AgentCard, but run-lifecycle
// webhooks are platform governance: the runtime asks them before it branches to
// the peer, so they are admitted. The other native-only fields keep being
// rejected.
func TestValidateAgentExternalWebhooks(t *testing.T) {
	external := func(mutate func(*agent.AgentSpec)) *agent.AgentSpec {
		spec := &agent.AgentSpec{
			Kind: agent.AgentKindExternal,
			Execution: &agent.AgentExecution{
				Mode: agent.ExecutionModeA2A,
				A2A: &agent.A2AExecution{
					Source:  agent.A2ASourceCardURL,
					CardURL: "https://peer.example.com/.well-known/agent-card.json",
				},
			},
		}
		mutate(spec)
		return spec
	}

	errs := ValidateAgent(external(func(spec *agent.AgentSpec) {
		spec.Webhooks = []string{"audit", "quota"}
	}), field.NewPath("spec"))
	assert.Empty(t, errs)

	errs = ValidateAgent(external(func(spec *agent.AgentSpec) {
		spec.Skills = []agent.Reference{{Name: "lint"}}
	}), field.NewPath("spec"))
	assert.True(t, containsField(errs, "spec.kind"),
		"native behaviour fields must still be rejected for an external agent, got %v", errs)
}

// ValidateAgent is the admission boundary for every resource an agent can name.
// The rows here are the rules that interact with each other, plus the shapes
// that parse field-by-field but describe something the controller can never
// resolve — those are the ones that get admitted and then quietly do nothing.
func TestValidateAgentReferences(t *testing.T) {
	promptRefs := func(n int) []agent.Reference {
		refs := make([]agent.Reference, 0, n)
		for i := 0; i < n; i++ {
			refs = append(refs, agent.Reference{Name: fmt.Sprintf("p%d", i)})
		}
		return refs
	}
	bogusSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "k", Operator: "Bogus"}},
	}

	tests := []struct {
		name    string
		spec    *agent.AgentSpec
		wantErr string
	}{
		{name: "an empty spec is valid", spec: &agent.AgentSpec{}},
		{
			name: "exactly the explicit prompt cap",
			spec: &agent.AgentSpec{Prompts: promptRefs(maxPromptRefs)},
		},
		{
			// The two spellings of the same list share one budget. Counting
			// them separately would admit 17 sources through a cap of 16.
			name: "the prompt cap is counted across promptRef and prompts",
			spec: &agent.AgentSpec{
				PromptRef: &agent.Reference{Name: "single"},
				Prompts:   promptRefs(maxPromptRefs),
			},
			wantErr: "spec.prompts",
		},
		{
			// The name shape is what tells a Prompt name from inline text on
			// the admin compatibility path.
			name:    "a prompt name that is not a DNS subdomain",
			spec:    &agent.AgentSpec{Prompts: []agent.Reference{{Name: "You are helpful"}}},
			wantErr: "spec.prompts[0].name",
		},
		{
			// An Agent spec names objects in its own namespace. Cross-namespace
			// capability is injected by an AgentBinding, where the target's
			// ReferenceGrant is part of the same object that names it.
			name:    "a cross-namespace prompt reference is refused",
			spec:    &agent.AgentSpec{Prompts: []agent.Reference{{Name: "base", Namespace: "other"}}},
			wantErr: "spec.prompts[0].namespace",
		},
		{
			name:    "cross-namespace skills are refused",
			spec:    &agent.AgentSpec{Skills: []agent.Reference{{Name: "lint", Namespace: "other"}}},
			wantErr: "spec.skills[0].namespace",
		},
		{
			name:    "a model reference must name a model",
			spec:    &agent.AgentSpec{ModelRef: &agent.Reference{}},
			wantErr: "spec.modelRef.name",
		},
		{
			name:    "a negative skill budget",
			spec:    &agent.AgentSpec{MaxSkills: -1},
			wantErr: "spec.maxSkills",
		},
		{
			name:    "an empty approval pattern",
			spec:    &agent.AgentSpec{RequireApproval: []string{""}},
			wantErr: "spec.requireApproval[0]",
		},
		{
			name: "an over-long regenerate token",
			spec: &agent.AgentSpec{PromptAggregation: &agent.PromptAggregationSpec{
				RegenerateToken: strings.Repeat("x", 129),
			}},
			wantErr: "spec.promptAggregation.regenerateToken",
		},
		{
			name:    "an unparseable skill selector",
			spec:    &agent.AgentSpec{SkillSelector: bogusSelector},
			wantErr: "spec.skillSelector",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateAgent(tc.spec, field.NewPath("spec"))
			if tc.wantErr == "" {
				assert.Empty(t, errs)
				return
			}
			assert.True(t, containsField(errs, tc.wantErr), "want an error on %s, got %v", tc.wantErr, errs)
		})
	}
}
