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
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	"github.com/onexstack/onexai/internal/agent/store"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/declclient"
)

// agentBindingStore serves bindings straight from the control plane.
//
// It is a concrete accessor rather than an instance of the generic Store: a
// binding has no second representation anywhere in the code base, so the row
// type and its conversions were overhead with no consumer. Reads keep using the
// informer cache; writes go through the typed clientset, like every other
// control-plane accessor in this package.
type agentBindingStore struct {
	client    *declclient.Client
	namespace string
}

var _ store.AgentBindingStore = (*agentBindingStore)(nil)

// NewAgentBindingStore builds the agent-binding accessor.
func NewAgentBindingStore(client *declclient.Client, namespace string) *agentBindingStore {
	return &agentBindingStore{client: client, namespace: namespace}
}

// Create implements store.AgentBindingStore.
func (s *agentBindingStore) Create(
	ctx context.Context, obj *agentv1alpha1.AgentBinding,
) (*agentv1alpha1.AgentBinding, error) {
	// The object's own namespace wins; then the configured namespace; then the
	// default namespace. This is the same policy every other store uses.
	ns := rowNamespace(obj.Namespace, s.namespace)
	obj.Namespace = ns
	return s.client.Clientset.AgentV1alpha1().AgentBindings(ns).Create(ctx, obj, metav1.CreateOptions{})
}

// Update implements store.AgentBindingStore.
func (s *agentBindingStore) Update(
	ctx context.Context, obj *agentv1alpha1.AgentBinding,
) (*agentv1alpha1.AgentBinding, error) {
	// The object lives in its own namespace, which is not the store's
	// configured one when that is empty (a cluster-wide read scope): a write
	// addressed at the wrong namespace is answered with 404, not with the object.
	ns, err := resolveNamespace(s.all, obj.Name, rowNamespace(obj.Namespace, s.namespace))
	if err != nil {
		return nil, err
	}
	api := s.client.Clientset.AgentV1alpha1().AgentBindings(ns)

	var updated *agentv1alpha1.AgentBinding
	// The update callback re-reads the object before writing, so another writer
	// (the controller patching status, or a second replica) only costs a retry
	// rather than a failed call.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := api.Get(ctx, obj.Name, metav1.GetOptions{}) //nolint:kubelistcheck // must not read a stale cache
		if err != nil {
			return err
		}
		// Only the writable fields are replaced: resourceVersion, annotations,
		// finalizers and ownerReferences have to survive the write, and status
		// belongs to the controller.
		obj.Spec.DeepCopyInto(&current.Spec)
		current.SetLabels(obj.Labels)
		updated, err = api.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// Delete implements store.AgentBindingStore.
func (s *agentBindingStore) Delete(ctx context.Context, name string) error {
	ns, err := resolveNamespace(s.all, name, writeNamespace(s.namespace))
	if err != nil {
		return err
	}
	// Deleting something already gone is success; a retried delete must not
	// surface as an error.
	return ignoreNotFound(s.client.Clientset.AgentV1alpha1().AgentBindings(ns).Delete(ctx, name, metav1.DeleteOptions{}))
}

// Get implements store.AgentBindingStore.
func (s *agentBindingStore) Get(ctx context.Context, name string) (*agentv1alpha1.AgentBinding, error) {
	if item, err := s.client.AgentBindings().AgentBindings(s.namespace).Get(name); err == nil {
		return item.DeepCopy(), nil
	} else if !isNotFound(err) {
		return nil, err
	}

	// The fast path may be namespace-scoped while the object lives elsewhere (or
	// the scope is empty, which matches nothing in the cache). Narrowing the
	// lookup silently would be worse than a slow one.
	items, err := s.all()
	if err != nil {
		return nil, err
	}
	item, found, err := uniqueByName(items, name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return item.DeepCopy(), nil
}

// List implements store.AgentBindingStore.
//
// The listing is cluster-wide, whatever the configured namespace: that is what
// the admin API has always answered, and narrowing it here would hide bindings
// a caller could still address by name.
func (s *agentBindingStore) List(
	ctx context.Context, offset, limit int,
) (int64, []*agentv1alpha1.AgentBinding, error) {
	items, err := s.all()
	if err != nil {
		return 0, nil, err
	}
	rows := make([]*agentv1alpha1.AgentBinding, 0, len(items))
	for _, item := range items {
		rows = append(rows, item.DeepCopy())
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	total := int64(len(rows))
	if offset > 0 {
		if offset >= len(rows) {
			rows = nil
		} else {
			rows = rows[offset:]
		}
	}
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	return total, rows, nil
}

// all returns the cluster-wide cache contents.
func (s *agentBindingStore) all() ([]*agentv1alpha1.AgentBinding, error) {
	return s.client.AgentBindings().List(labels.Everything())
}
