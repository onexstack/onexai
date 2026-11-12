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

package store

import (
	"context"

	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// ClusterAgentBindingStore persists bindings whose audience spans namespaces.
//
// It is a separate accessor rather than a namespaced one with an empty
// namespace: a cluster-scoped object has no namespace to resolve, and reusing
// the namespaced path would mean every call site had to know that "" is a
// legitimate value rather than a missing one.
type ClusterAgentBindingStore interface {
	// Create writes a new binding and returns the object the server stored.
	Create(ctx context.Context, obj *agentv1alpha1.ClusterAgentBinding) (*agentv1alpha1.ClusterAgentBinding, error)
	// Update rewrites the writable fields of an existing binding.
	Update(ctx context.Context, obj *agentv1alpha1.ClusterAgentBinding) (*agentv1alpha1.ClusterAgentBinding, error)
	// Delete removes the named binding. Deleting an absent object is success.
	Delete(ctx context.Context, name string) error
	// Get returns the named binding, or (nil, nil) when it does not exist.
	Get(ctx context.Context, name string) (*agentv1alpha1.ClusterAgentBinding, error)
	// List returns one page of bindings ordered by name, plus the total count.
	List(ctx context.Context, offset, limit int) (int64, []*agentv1alpha1.ClusterAgentBinding, error)

	ClusterAgentBindingExpansion
}

// ClusterAgentBindingExpansion holds binding-specific operations that are not
// the standard five.
type ClusterAgentBindingExpansion interface{}
