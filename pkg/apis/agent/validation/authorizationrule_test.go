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
	"testing"

	"github.com/stretchr/testify/assert"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"

	agent "github.com/onexstack/onexai/pkg/apis/agent"
)

func TestValidateAuthorizationRule(t *testing.T) {
	valid := func() *agent.AuthorizationRuleSpec {
		return &agent.AuthorizationRuleSpec{
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: "onexai-sre"}},
			AgentNames: []string{"sre-helper"},
			Resource:   agent.AuthorizationResource{Kind: "tool", Names: []string{"kubectl_*"}},
			Verbs:      []string{"use"},
			Effect:     agent.EffectAllow,
			Enabled:    true,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*agent.AuthorizationRuleSpec)
		wantErr string
	}{
		{name: "valid rule", mutate: func(*agent.AuthorizationRuleSpec) {}},
		{
			name:    "no subjects",
			mutate:  func(s *agent.AuthorizationRuleSpec) { s.Subjects = nil },
			wantErr: "spec.subjects",
		},
		{
			name: "service account without namespace",
			mutate: func(s *agent.AuthorizationRuleSpec) {
				s.Subjects = []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "agent"}}
			},
			wantErr: "spec.subjects[0].namespace",
		},
		{
			name:    "unknown resource kind",
			mutate:  func(s *agent.AuthorizationRuleSpec) { s.Resource.Kind = "deployment" },
			wantErr: "spec.resource.kind",
		},
		{
			name:    "no resource names",
			mutate:  func(s *agent.AuthorizationRuleSpec) { s.Resource.Names = nil },
			wantErr: "spec.resource.names",
		},
		{
			name:    "unknown verb",
			mutate:  func(s *agent.AuthorizationRuleSpec) { s.Verbs = []string{"fly"} },
			wantErr: "spec.verbs[0]",
		},
		{
			name:    "unknown effect",
			mutate:  func(s *agent.AuthorizationRuleSpec) { s.Effect = "Maybe" },
			wantErr: "spec.effect",
		},
		{
			name: "attributes must be an object",
			mutate: func(s *agent.AuthorizationRuleSpec) {
				s.Attributes = &runtime.RawExtension{Raw: []byte(`["prod"]`)}
			},
			wantErr: "spec.attributes",
		},
		{
			name: "attribute values must be strings",
			mutate: func(s *agent.AuthorizationRuleSpec) {
				s.Attributes = &runtime.RawExtension{Raw: []byte(`{"env":1}`)}
			},
			wantErr: "spec.attributes[env]",
		},
		{
			name: "bad selector",
			mutate: func(s *agent.AuthorizationRuleSpec) {
				s.AgentSelector = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "tier", Operator: "Bogus", Values: []string{"prod"}},
				}}
			},
			wantErr: "spec.agentSelector",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := valid()
			tt.mutate(spec)
			errs := ValidateAuthorizationRule(spec, field.NewPath("spec"))

			if tt.wantErr == "" {
				assert.Empty(t, errs)
				return
			}
			assert.NotEmpty(t, errs)
			assert.Equal(t, tt.wantErr, errs[0].Field)
		})
	}
}
