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
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// TestRuleSourceLive exercises the adapter against a running oai-declserver.
//
// It is skipped unless ONEXAI_CONTROLPLANE_KUBECONFIG points at a kubeconfig
// for one: the test creates and deletes a real AuthorizationRule, which is only
// safe on a disposable control plane.
func TestRuleSourceLive(t *testing.T) {
	kubeconfig := os.Getenv("ONEXAI_CONTROLPLANE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set ONEXAI_CONTROLPLANE_KUBECONFIG to run against a live oai-declserver")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := declclient.New(kubeconfig, "default", 30*time.Second)
	require.NoError(t, err)

	source := NewSource(client)
	require.NoError(t, client.Start(ctx, 10*time.Second))

	rules := client.Clientset.AgentV1alpha1().AuthorizationRules("default")
	rule := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "live-source-test", Namespace: "default"},
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Subjects: []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: "onexai-sre"}},
			Resource: agentv1alpha1.AuthorizationResource{Kind: "tool", Names: []string{"kubectl_*"}},
			Verbs:    []string{"use", "list"},
			Effect:   agentv1alpha1.EffectAllow,
			Enabled:  true,
			Attributes: &runtime.RawExtension{
				Raw: []byte(`{"env":"prod"}`),
			},
		},
	}

	createdAt := time.Now()
	created, err := rules.Create(ctx, rule, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = rules.Delete(context.Background(), created.Name, metav1.DeleteOptions{})
	})

	// The watcher may deliver a signal from an unrelated reconcile first, so the
	// assertion is on the lister's content rather than on one event.
	row := waitForRow(t, ctx, source, rule.Name, func(row *model.AuthzRuleM) bool {
		return row.SubjectType == "group"
	})
	assert.Equal(t, "tool", row.ResourceKind)
	assert.Equal(t, `{"env":"prod"}`, row.ResourceAttributes)
	assert.ElementsMatch(t, []string{"use", "read"}, []string(row.Verbs))
	assert.Equal(t, "allow", row.Effect)
	assert.Empty(t, row.Status)
	// The acceptance criterion for the migration: a rule edit reaches every
	// replica through the watch, not after a poll interval.
	assert.Less(t, time.Since(createdAt), time.Second, "a rule change must propagate within a second")

	// A disabled rule must survive as a row carrying the disabled status: the
	// rule store drops it, and doing that here would hide the reason.
	// The controller patches status on its own schedule, so the update must
	// re-read and retry rather than write a stale resourceVersion back.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := rules.Get(ctx, created.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		latest.Spec.Enabled = false
		_, err = rules.Update(ctx, latest, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err)

	row = waitForRow(t, ctx, source, rule.Name, func(row *model.AuthzRuleM) bool {
		return row.SubjectType == "group" && row.Status == model.StatusDisabled
	})
	assert.Equal(t, model.StatusDisabled, row.Status)

	// A selector must be resolved to agent names at load time: the Rego data has
	// no notion of a selector, and an unresolved selector would silently become
	// an unscoped (i.e. global) rule.
	agents := client.Clientset.AgentV1alpha1().Agents("default")
	selectorAgent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "live-selector-agent",
			Namespace: "default",
			Labels:    map[string]string{"onexai.io/tier": "live-test"},
		},
	}
	_, err = agents.Create(ctx, selectorAgent, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = agents.Delete(context.Background(), selectorAgent.Name, metav1.DeleteOptions{})
	})

	selectorRule := &agentv1alpha1.AuthorizationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "live-selector-rule", Namespace: "default"},
		Spec: agentv1alpha1.AuthorizationRuleSpec{
			Subjects:      []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "go.kong"}},
			AgentSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"onexai.io/tier": "live-test"}},
			Resource:      agentv1alpha1.AuthorizationResource{Kind: "agent", Names: []string{"*"}},
			Verbs:         []string{"use"},
			Effect:        agentv1alpha1.EffectAllow,
			Enabled:       true,
		},
	}
	// Wait until the agent informer sees the new agent: the rule's selector is
	// resolved against that cache, and creating the rule first would (correctly)
	// resolve to nobody until the agent event arrives.
	require.Eventually(t, func() bool {
		_, err := client.Agents().Agents("default").Get(selectorAgent.Name)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "agent never appeared in the informer cache")

	_, err = rules.Create(ctx, selectorRule, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = rules.Delete(context.Background(), selectorRule.Name, metav1.DeleteOptions{})
	})

	selected := waitForRow(t, ctx, source, selectorRule.Name, func(row *model.AuthzRuleM) bool {
		return row.SubjectType == "user"
	})
	assert.Equal(t, []string{"live-selector-agent"}, []string(selected.AgentNames))
}

// waitForRow polls the source until a row of the named rule matches, which keeps
// the test independent of event ordering (a status patch from the controller and
// the rule's own add both produce change signals).
func waitForRow(t *testing.T, ctx context.Context, source *Source, ruleName string, match func(*model.AuthzRuleM) bool) *model.AuthzRuleM {
	t.Helper()

	var found *model.AuthzRuleM
	require.Eventually(t, func() bool {
		_, rows, err := source.List(ctx, nil)
		if err != nil {
			return false
		}
		for _, row := range rows {
			if strings.HasPrefix(row.Name, ruleName+"/") && match(row) {
				found = row
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "no matching row for rule %q", ruleName)
	return found
}
