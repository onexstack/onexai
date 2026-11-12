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

// Package grant builds the per-reconcile ReferenceGrant view shared by the
// Agent and AgentBinding controllers.
package grant

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/onexstack/onexai/pkg/apis/agent/sharing"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
)

// AgentKind is the kind a grant's from list names. Every cross-namespace
// capability is ultimately used by an Agent, whether the reference lives in
// the Agent or in a binding.
const AgentKind = "Agent"

// Index snapshots every ReferenceGrant for one reconcile.
func Index(ctx context.Context, c client.Client) (*sharing.GrantIndex, error) {
	var list agentv1alpha1.ReferenceGrantList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list reference grants: %w", err)
	}
	grants := make([]sharing.Grant, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		for _, from := range item.Spec.From {
			for _, to := range item.Spec.To {
				grants = append(grants, sharing.Grant{
					Namespace:     item.Namespace,
					FromGroup:     from.Group,
					FromKind:      from.Kind,
					FromNamespace: from.Namespace,
					ToGroup:       to.Group,
					ToKind:        to.Kind,
					ToName:        to.Name,
				})
			}
		}
	}
	return sharing.NewGrantIndex(grants), nil
}

// Allows reports whether a grant permits referrerNamespace to use the target.
// Same-namespace references are always allowed and need no grant.
func Allows(index *sharing.GrantIndex, referrerNamespace, targetNamespace, targetKind, targetName string) bool {
	if referrerNamespace == targetNamespace {
		return true
	}
	if index == nil {
		return false
	}
	return index.Allows(
		sharing.Ref{Group: sharing.GroupName, Kind: AgentKind, Namespace: referrerNamespace},
		sharing.Ref{Group: sharing.GroupName, Kind: targetKind, Namespace: targetNamespace, Name: targetName},
	)
}
