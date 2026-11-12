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

// clusterAgentBindingStore serves cluster-wide bindings straight from the
// control plane.
//
// It has no namespace field on purpose. A cluster-scoped object has no
// namespace to resolve and no fallback to apply; carrying one would suggest a
// scope that does not exist.
type clusterAgentBindingStore struct {
	client *declclient.Client
}

var _ store.ClusterAgentBindingStore = (*clusterAgentBindingStore)(nil)

// NewClusterAgentBindingStore builds the cluster-binding accessor.
func NewClusterAgentBindingStore(client *declclient.Client) *clusterAgentBindingStore {
	return &clusterAgentBindingStore{client: client}
}

// Create implements store.ClusterAgentBindingStore.
func (s *clusterAgentBindingStore) Create(
	ctx context.Context, obj *agentv1alpha1.ClusterAgentBinding,
) (*agentv1alpha1.ClusterAgentBinding, error) {
	return s.client.Clientset.AgentV1alpha1().ClusterAgentBindings().Create(ctx, obj, metav1.CreateOptions{})
}

// Update implements store.ClusterAgentBindingStore.
func (s *clusterAgentBindingStore) Update(
	ctx context.Context, obj *agentv1alpha1.ClusterAgentBinding,
) (*agentv1alpha1.ClusterAgentBinding, error) {
	api := s.client.Clientset.AgentV1alpha1().ClusterAgentBindings()

	var updated *agentv1alpha1.ClusterAgentBinding
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := api.Get(ctx, obj.Name, metav1.GetOptions{}) //nolint:kubelistcheck // must not read a stale cache
		if err != nil {
			return err
		}
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

// Delete implements store.ClusterAgentBindingStore.
func (s *clusterAgentBindingStore) Delete(ctx context.Context, name string) error {
	return ignoreNotFound(s.client.Clientset.AgentV1alpha1().ClusterAgentBindings().Delete(ctx, name, metav1.DeleteOptions{}))
}

// Get implements store.ClusterAgentBindingStore.
func (s *clusterAgentBindingStore) Get(ctx context.Context, name string) (*agentv1alpha1.ClusterAgentBinding, error) {
	item, err := s.client.ClusterAgentBindings().Get(name)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return item.DeepCopy(), nil
}

// List implements store.ClusterAgentBindingStore.
func (s *clusterAgentBindingStore) List(
	ctx context.Context, offset, limit int,
) (int64, []*agentv1alpha1.ClusterAgentBinding, error) {
	items, err := s.client.ClusterAgentBindings().List(labels.Everything())
	if err != nil {
		return 0, nil, err
	}
	rows := make([]*agentv1alpha1.ClusterAgentBinding, 0, len(items))
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
