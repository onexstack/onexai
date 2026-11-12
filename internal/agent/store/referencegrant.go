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

// ReferenceGrantStore persists cross-namespace sharing grants.
//
// Like the binding store it speaks the control-plane object directly: a grant
// is never stored anywhere but the declarative control plane.
type ReferenceGrantStore interface {
	Create(ctx context.Context, obj *agentv1alpha1.ReferenceGrant) (*agentv1alpha1.ReferenceGrant, error)
	Update(ctx context.Context, obj *agentv1alpha1.ReferenceGrant) (*agentv1alpha1.ReferenceGrant, error)
	Delete(ctx context.Context, name string) error
	Get(ctx context.Context, name string) (*agentv1alpha1.ReferenceGrant, error)
	List(ctx context.Context, offset, limit int) (int64, []*agentv1alpha1.ReferenceGrant, error)

	ReferenceGrantExpansion
}

// ReferenceGrantExpansion holds grant-specific operations that are not the
// standard five.
type ReferenceGrantExpansion interface {
	// ListLive reads every grant straight from the control plane, bypassing the
	// informer. It exists for the gap between a grant being created and the
	// watch delivering it: a run refused from the cache must not be refused by
	// a grant that is already persisted.
	ListLive(ctx context.Context) ([]*agentv1alpha1.ReferenceGrant, error)
}
