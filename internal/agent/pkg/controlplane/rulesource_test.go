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

package controlplane

import (
	"testing"

	"github.com/stretchr/testify/assert"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

func testRule() *agentv1alpha1.AuthorizationRule {
	return &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-read", Namespace: "default"},
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Subjects: []rbacv1.Subject{
				{Kind: "Group", Name: "sre"},
				{Kind: "User", Name: "go.kong"},
			},
			Resource: agentv1alpha1.AuthorizationResource{Kind: "Model", Names: []string{"deepseek"}},
			Verbs:    []string{"get", "list"},
			Effect:   agentv1alpha1.EffectAllow,
			Enabled:  true,
		},
	}
}

// TestChangeAffectsLoadKeepsStatusUpdatesQuiet pins the filter that keeps the
// reloader from re-listing the rule set on every unrelated write. Status and
// resourceVersion are written by other controllers — the health reporter
// rewrites agent status continuously — and none of it is read by this source.
func TestChangeAffectsLoadKeepsStatusUpdatesQuiet(t *testing.T) {
	t.Run("rule status update is not a change", func(t *testing.T) {
		oldObj := testRule()
		newObj := testRule()
		newObj.ResourceVersion = "42"
		newObj.Status.Conditions = nil

		assert.False(t, changeAffectsLoad(oldObj, newObj))
	})

	t.Run("rule spec edit is a change", func(t *testing.T) {
		oldObj := testRule()
		newObj := testRule()
		newObj.Spec.Verbs = []string{"get", "list", "create"}

		assert.True(t, changeAffectsLoad(oldObj, newObj))
	})

	t.Run("agent status update is not a change", func(t *testing.T) {
		oldObj := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
			Name: "helper", Namespace: "default", Labels: map[string]string{"tier": "vip"},
		}}
		newObj := oldObj.DeepCopy()
		newObj.ResourceVersion = "7"
		newObj.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}

		assert.False(t, changeAffectsLoad(oldObj, newObj))
	})

	t.Run("agent relabel is a change", func(t *testing.T) {
		oldObj := &agentv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
			Name: "helper", Namespace: "default", Labels: map[string]string{"tier": "vip"},
		}}
		newObj := oldObj.DeepCopy()
		newObj.Labels["tier"] = "free"

		assert.True(t, changeAffectsLoad(oldObj, newObj))
	})

	t.Run("an unknown kind is a change", func(t *testing.T) {
		assert.True(t, changeAffectsLoad(struct{}{}, struct{}{}))
	})
}

// TestExpandIsStable pins the ordering of the rows a rule expands into. The
// store compares generations with reflect.DeepEqual to decide whether the rule
// set changed, and an unstable order made every comparison report a change —
// which re-prepared the policy and dropped the decision cache on every reload.
func TestExpandIsStable(t *testing.T) {
	rule := testRule()

	first := expand(rule, []string{"helper"})
	for i := 0; i < 50; i++ {
		assert.Equal(t, first, expand(rule, []string{"helper"}))
	}

	// One row per subject kind, in sorted order, plus the namespace row when the
	// rule declares one.
	names := make([]string, 0, len(first))
	for _, row := range first {
		names = append(names, row.Name)
	}
	assert.Equal(t, []string{"allow-read/group", "allow-read/user"}, names)
}

// TestExpandMultiResourcePolicy pins the plural shape: one policy naming a
// user and a group, and a namespace and two agent names, becomes one row per
// (subject kind, resource kind). Folding resources into one row would let the
// last one win.
func TestExpandMultiResourcePolicy(t *testing.T) {
	rule := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "dept-ai-policy"},
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Subjects: []rbacv1.Subject{
				{Kind: "User", Name: "uuid-alice"},
				{Kind: "Group", Name: "dept-ai"},
			},
			Resources: []agentv1alpha1.AuthorizationResource{
				{Kind: "namespace", Names: []string{"helper-public"}},
				{Kind: "agent", Names: []string{"researcher", "reviewer"}},
			},
			Verbs:   []string{"get"},
			Effect:  agentv1alpha1.EffectAllow,
			Enabled: true,
		},
	}

	rows := expand(rule, nil)
	// 2 subject kinds × 2 resource entries.
	assert.Len(t, rows, 4)

	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.SubjectType+"/"+row.ResourceKind] = true
		if row.SubjectType == "group" {
			assert.Equal(t, []string{"dept-ai"}, []string(row.SubjectID))
		}
		if row.SubjectType == "user" {
			assert.Equal(t, []string{"uuid-alice"}, []string(row.SubjectID))
		}
		assert.Contains(t, row.Verbs, "read")
	}
	assert.True(t, seen["user/namespace"])
	assert.True(t, seen["user/agent"])
	assert.True(t, seen["group/namespace"])
	assert.True(t, seen["group/agent"])

	// Multi-resource row names are deterministic and distinct.
	names := map[string]bool{}
	for _, row := range rows {
		assert.False(t, names[row.Name], "duplicate row name %q", row.Name)
		names[row.Name] = true
	}
}

// TestExpandLegacyRuleKeepsRowNames guards backward compatibility: a rule that
// only sets the singular resource keeps the row names existing logs and tests
// depend on.
func TestExpandLegacyRuleKeepsRowNames(t *testing.T) {
	rows := expand(testRule(), []string{"helper"})
	assert.NotEmpty(t, rows)
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	assert.ElementsMatch(t, []string{"allow-read/group", "allow-read/user"}, names)
}
