// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PeerBinding records what a chat peer runs and the subject its runs are
// authorized as. (backend, scope, peerID) is the identity; canonical is the
// subject a business system recognizes.
type PeerBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec PeerBindingSpec `json:"spec,omitempty"`
	// +optional
	Status PeerBindingStatus `json:"status,omitempty"`
}

// PeerBindingSpec defines the desired state of PeerBinding.
type PeerBindingSpec struct {
	CommonSpec `json:",inline"`

	// Backend is the class of caller that produced PeerID: "lark" or "openapi".
	Backend string `json:"backend"`

	// Scope is how far PeerID is unique inside Backend. Lark fills it with the
	// application id; OpenAPI leaves it empty.
	// +optional
	Scope string `json:"scope,omitempty"`

	// PeerID is the chat platform's name for the person (Lark: open_id).
	PeerID string `json:"peerID"`

	// Canonical is the subject runs are authorized as.
	// +optional
	Canonical string `json:"canonical,omitempty"`

	// Agents names the agents this peer runs. An empty list is a valid state —
	// bound, with nothing usable — and is different from having no binding.
	// +optional
	Agents []string `json:"agents,omitempty"`

	// Source names whoever wrote the binding, from the credential it
	// authenticated with. It is for audit: with several integrators, "which of
	// them pushed this, and as what" is the first question asked.
	// +optional
	Source string `json:"source,omitempty"`
}

// PeerBindingStatus defines the observed state of PeerBinding.
type PeerBindingStatus struct {
	CommonStatus `json:",inline"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PeerBindingList is a list of PeerBinding objects.
type PeerBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []PeerBinding `json:"items"`
}
