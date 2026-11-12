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

package controlplanestore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
	versionedfake "github.com/onexstack/onexai/pkg/generated/clientset/versioned/fake"
	informers "github.com/onexstack/onexai/pkg/generated/informers/externalversions"
)

// newTestClient builds a client over a fake API server. The informer factory is
// deliberately never started: every write path below resolves the object it is
// touching from what it just read, so an empty cache only exercises the
// fallback that production uses when its cache has not synced.
func newTestClient(objs ...runtime.Object) (*declclient.Client, *versionedfake.Clientset) {
	client := versionedfake.NewSimpleClientset(objs...)
	return &declclient.Client{
		Clientset: client,
		Factory:   informers.NewSharedInformerFactory(client, 0),
	}, client
}

func updateActions(client *versionedfake.Clientset, resource string) int {
	count := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" && action.GetResource().Resource == resource {
			count++
		}
	}
	return count
}

func TestAgentStoreUpdateKeepsWebhooksWhenRowHasNoOpinion(t *testing.T) {
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "onex-ops-console", Namespace: "onex", UID: types.UID("agent-uid")},
		Spec: agentv1alpha1.AgentSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true, DisplayName: "onex-ops-console"},
			Webhooks:   []string{"audit"},
		},
	}
	client, cs := newTestClient(agent)
	store := NewAgentStore(client, "")
	row := &model.AgentM{Name: "onex-ops-console", DisplayName: "onex-ops-console", Namespace: "onex"}

	require.NoError(t, store.Update(context.Background(), row))
	got, err := cs.AgentV1alpha1().Agents("onex").Get(context.Background(), "onex-ops-console", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"audit"}, got.Spec.Webhooks, "a nil row value means no opinion, not clear them")

	// An explicitly empty (but non-nil) list does mean clear them, and the no-op
	// check must not swallow it.
	row.Webhooks = model.StringList{}
	require.NoError(t, store.Update(context.Background(), row))
	got, err = cs.AgentV1alpha1().Agents("onex").Get(context.Background(), "onex-ops-console", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, got.Spec.Webhooks)
}

func TestAgentStoreUpdateTemplateRefsNilVsEmpty(t *testing.T) {
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "onex-ops-console", Namespace: "onex", UID: types.UID("agent-uid")},
		Spec: agentv1alpha1.AgentSpec{
			CommonSpec:   agentv1alpha1.CommonSpec{Enabled: true, DisplayName: "onex-ops-console"},
			TemplateRefs: []agentv1alpha1.Reference{{Name: "base"}},
		},
	}
	client, cs := newTestClient(agent)
	store := NewAgentStore(client, "")
	row := &model.AgentM{Name: "onex-ops-console", DisplayName: "onex-ops-console", Namespace: "onex"}

	require.NoError(t, store.Update(context.Background(), row))
	got, err := cs.AgentV1alpha1().Agents("onex").Get(context.Background(), "onex-ops-console", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []agentv1alpha1.Reference{{Name: "base"}}, got.Spec.TemplateRefs,
		"a nil row value means no opinion, not clear the bindings")

	// An explicitly empty (but non-nil) list does mean clear them.
	row.TemplateRefs = model.StringList{}
	require.NoError(t, store.Update(context.Background(), row))
	got, err = cs.AgentV1alpha1().Agents("onex").Get(context.Background(), "onex-ops-console", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, got.Spec.TemplateRefs)
}

func TestAgentStoreUpdateLeavesPromptSelectorAlone(t *testing.T) {
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "onex-ops-console", Namespace: "onex", UID: types.UID("agent-uid")},
		Spec: agentv1alpha1.AgentSpec{
			CommonSpec:     agentv1alpha1.CommonSpec{Enabled: true, DisplayName: "onex-ops-console"},
			PromptSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "sre"}},
		},
	}
	client, cs := newTestClient(agent)
	store := NewAgentStore(client, "")
	row := &model.AgentM{Name: "onex-ops-console", DisplayName: "onex-ops-console", Namespace: "onex"}

	require.NoError(t, store.Update(context.Background(), row))
	got, err := cs.AgentV1alpha1().Agents("onex").Get(context.Background(), "onex-ops-console", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"team": "sre"}, got.Spec.PromptSelector.MatchLabels,
		"the management API cannot express a selector, so a nil list is no opinion")
}

func TestAgentStoreUpdateWritesRealChange(t *testing.T) {
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "onex-ops-console", Namespace: "onex", UID: types.UID("agent-uid")},
		Spec: agentv1alpha1.AgentSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true, DisplayName: "old name"},
		},
	}
	client, cs := newTestClient(agent)
	store := NewAgentStore(client, "")
	row := &model.AgentM{Name: "onex-ops-console", DisplayName: "new name", Namespace: "onex"}

	require.NoError(t, store.Update(context.Background(), row))
	assert.Equal(t, 1, updateActions(cs, "agents"))

	got, err := cs.AgentV1alpha1().Agents("onex").Get(context.Background(), "onex-ops-console", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "new name", got.Spec.DisplayName)
}

// TestAgentStoreUpdateRetriesOnConflict pins the property the console relies on:
// two messages arriving together both rewrite the same Agent, and the loser's
// conflict is retried rather than surfaced as a failure.
func TestAgentStoreUpdateRetriesOnConflict(t *testing.T) {
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "onex-ops-console", Namespace: "onex", UID: types.UID("agent-uid")},
		Spec: agentv1alpha1.AgentSpec{
			CommonSpec: agentv1alpha1.CommonSpec{Enabled: true, DisplayName: "old"},
		},
	}
	client, cs := newTestClient(agent)

	conflicts := 1
	cs.PrependReactor("update", "agents", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts == 0 {
			return false, nil, nil
		}
		conflicts--
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Group: agentv1alpha1.GroupName, Resource: "agents"}, "onex-ops-console", nil)
	})

	store := NewAgentStore(client, "")
	err := store.Update(context.Background(), &model.AgentM{
		Name:        "onex-ops-console",
		DisplayName: "new",
		Status:      model.StatusEnabled,
		Namespace:   "onex",
	})
	require.NoError(t, err)

	got, err := cs.AgentV1alpha1().Agents("onex").Get(context.Background(), "onex-ops-console", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "new", got.Spec.DisplayName, "the retry must end with the change applied")
}
