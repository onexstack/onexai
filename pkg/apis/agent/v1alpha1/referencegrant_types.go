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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReferenceGrant permits objects in the listed namespaces to reference objects
// in this grant's namespace.
//
// It is the onexai counterpart of the Gateway API ReferenceGrant: the grant
// lives in the target's namespace, its owner creates it, and it is purely
// additive. Same-namespace references never need a grant.
//
// A grant carries no status. Whether it takes effect is reported by the
// referrer (the Agent), not by the grant itself: the owner withdraws access by
// deleting or narrowing the grant.
//
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ReferenceGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec describes who may reference what in this namespace.
	// +optional
	Spec ReferenceGrantSpec `json:"spec,omitempty"`
}

// ReferenceGrantSpec describes the two sides of the grant.
type ReferenceGrantSpec struct {
	// From lists the referrers. A grant whose From list is empty permits
	// nobody, which is the fail-closed default.
	// +optional
	From []ReferenceGrantFrom `json:"from,omitempty"`

	// To lists what may be referenced. A grant whose To list is empty exposes
	// nothing, which is the fail-closed default.
	// +optional
	To []ReferenceGrantTo `json:"to,omitempty"`
}

// ReferenceGrantFrom identifies the referrer side.
//
// There is deliberately no Name: an Agent-level assignment is the job of
// AgentBinding / ClusterAgentBinding, while a grant answers the namespace-level
// question "may this tenant's agents reach my object at all?".
type ReferenceGrantFrom struct {
	// Group is the API group of the referrer.
	Group string `json:"group"`

	// Kind is the kind of the referrer (normally Agent).
	Kind string `json:"kind"`

	// Namespace is the referrer's namespace.
	Namespace string `json:"namespace"`
}

// ReferenceGrantTo identifies the target side.
type ReferenceGrantTo struct {
	// Group is the API group of the target.
	Group string `json:"group"`

	// Kind is the kind of the target (Skill, MCPServer, KnowledgeBase, ...).
	Kind string `json:"kind"`

	// Name is the target's name. Empty means every object of this kind in the
	// grant's namespace.
	// +optional
	Name string `json:"name,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ReferenceGrantList is a list of ReferenceGrant objects.
type ReferenceGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of ReferenceGrant objects.
	Items []ReferenceGrant `json:"items"`
}
