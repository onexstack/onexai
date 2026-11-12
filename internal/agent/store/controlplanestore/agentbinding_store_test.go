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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"github.com/onexstack/onexai/internal/agent/model"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
	agentfake "github.com/onexstack/onexai/pkg/generated/clientset/versioned/fake"
	informers "github.com/onexstack/onexai/pkg/generated/informers/externalversions"
)

// newTestBindingStore builds the store over a fake clientset and an informer
// cache seeded with the given objects. The informer is never started: the tests
// populate its indexer directly, which is what the lister reads.
func newTestBindingStore(t *testing.T, namespace string, cached ...*agentv1alpha1.AgentBinding) (*agentBindingStore, *agentfake.Clientset) {
	t.Helper()

	objects := make([]runtime.Object, 0, len(cached))
	for _, obj := range cached {
		objects = append(objects, obj)
	}
	clientset := agentfake.NewSimpleClientset(objects...)
	factory := informers.NewSharedInformerFactory(clientset, time.Minute)
	indexer := factory.Agent().V1alpha1().AgentBindings().Informer().GetIndexer()
	for _, obj := range cached {
		require.NoError(t, indexer.Add(obj))
	}

	client := &declclient.Client{Clientset: clientset, Factory: factory}
	return NewAgentBindingStore(client, namespace), clientset
}

func TestAgentBindingCreateUsesTheObjectNamespace(t *testing.T) {
	store, clientset := newTestBindingStore(t, "default")

	created, err := store.Create(context.Background(), &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "team-a"},
	})
	require.NoError(t, err)
	assert.Equal(t, "team-a", created.Namespace)

	stored, err := clientset.AgentV1alpha1().AgentBindings("team-a").
		Get(context.Background(), "base", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "base", stored.Name)
}

func TestAgentBindingCreateFallsBackToTheConfiguredNamespace(t *testing.T) {
	store, clientset := newTestBindingStore(t, "onex")

	created, err := store.Create(context.Background(), &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "base"},
	})
	require.NoError(t, err)
	assert.Equal(t, "onex", created.Namespace)

	_, err = clientset.AgentV1alpha1().AgentBindings("onex").
		Get(context.Background(), "base", metav1.GetOptions{})
	require.NoError(t, err)
}

func TestAgentBindingCreateWithoutAnyNamespaceUsesTheDefaultNamespace(t *testing.T) {
	store, clientset := newTestBindingStore(t, "")

	created, err := store.Create(context.Background(), &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "base"},
	})
	require.NoError(t, err)
	assert.Equal(t, model.DefaultNamespace, created.Namespace)

	_, err = clientset.AgentV1alpha1().AgentBindings(model.DefaultNamespace).
		Get(context.Background(), "base", metav1.GetOptions{})
	require.NoError(t, err)
}

func TestAgentBindingGetReadsTheCache(t *testing.T) {
	store, _ := newTestBindingStore(t, "team-a",
		&agentv1alpha1.AgentBinding{ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "team-a"}})

	got, err := store.Get(context.Background(), "base")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "team-a", got.Namespace)

	// A miss answers (nil, nil), which the biz turns into a 404 — not an error.
	missing, err := store.Get(context.Background(), "ghost")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestAgentBindingGetFindsAnObjectOutsideTheConfiguredNamespace(t *testing.T) {
	// The configured scope is narrower than where the object lives; the fast path
	// misses and the cluster-wide scan has to answer.
	store, _ := newTestBindingStore(t, "default",
		&agentv1alpha1.AgentBinding{ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "team-a"}})

	got, err := store.Get(context.Background(), "base")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "team-a", got.Namespace)
}

func TestAgentBindingListSortsAndPages(t *testing.T) {
	store, _ := newTestBindingStore(t, "",
		&agentv1alpha1.AgentBinding{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "onex"}},
		&agentv1alpha1.AgentBinding{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "onex"}},
		&agentv1alpha1.AgentBinding{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "other"}},
	)

	total, page, err := store.List(context.Background(), 1, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total, "the count is the pre-page total")
	require.Len(t, page, 1)
	assert.Equal(t, "b", page[0].Name, "the listing is ordered by name")
}

func TestAgentBindingUpdateAddressesTheObjectsNamespace(t *testing.T) {
	existing := &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "base",
			Namespace:       "team-a",
			ResourceVersion: "42",
			Annotations:     map[string]string{"keep": "me"},
		},
		Spec:   agentv1alpha1.AgentBindingSpec{DisplayName: "old"},
		Status: agentv1alpha1.AgentBindingStatus{MatchedAgents: 7},
	}
	store, clientset := newTestBindingStore(t, "default", existing)

	updated, err := store.Update(context.Background(), &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Labels: map[string]string{"team": "sre"}},
		Spec:       agentv1alpha1.AgentBindingSpec{DisplayName: "new"},
	})
	require.NoError(t, err)
	assert.Equal(t, "team-a", updated.Namespace, "the write must land where the object lives")
	assert.Equal(t, "new", updated.Spec.DisplayName)
	assert.Equal(t, "sre", updated.Labels["team"])

	// Fields the caller does not own survive the write.
	assert.Equal(t, map[string]string{"keep": "me"}, updated.Annotations)
	assert.Equal(t, "42", updated.ResourceVersion)
	assert.Equal(t, int32(7), updated.Status.MatchedAgents)

	stored, err := clientset.AgentV1alpha1().AgentBindings("team-a").
		Get(context.Background(), "base", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "new", stored.Spec.DisplayName)
}

func TestAgentBindingUpdateRetriesOnConflict(t *testing.T) {
	store, clientset := newTestBindingStore(t, "default", &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "default"},
	})

	conflicts := 1
	updates := 0
	clientset.PrependReactor("update", "agentbindings", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		if conflicts > 0 {
			conflicts--
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: agentv1alpha1.GroupName, Resource: "agentbindings"},
				"base", nil)
		}
		return false, nil, nil
	})

	updated, err := store.Update(context.Background(), &agentv1alpha1.AgentBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "base"},
		Spec:       agentv1alpha1.AgentBindingSpec{DisplayName: "new"},
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "new", updated.Spec.DisplayName)
	assert.Equal(t, 2, updates, "a resourceVersion conflict costs one retry")
}

func TestAgentBindingDeleteIsIdempotentAndResolvesTheNamespace(t *testing.T) {
	store, clientset := newTestBindingStore(t, "default",
		&agentv1alpha1.AgentBinding{ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "team-a"}})

	require.NoError(t, store.Delete(context.Background(), "base"))
	_, err := clientset.AgentV1alpha1().AgentBindings("team-a").
		Get(context.Background(), "base", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the delete must have addressed team-a")

	// A retried delete is success, not a 404.
	require.NoError(t, store.Delete(context.Background(), "base"))
}
