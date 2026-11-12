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

package chat

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PeerBinding is the internal (hub) schema for the peerbindings API.
type PeerBinding struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   PeerBindingSpec
	Status PeerBindingStatus
}

// PeerBindingSpec defines the desired state of PeerBinding.
type PeerBindingSpec struct {
	CommonSpec

	Backend   string
	Scope     string
	PeerID    string
	Canonical string
	Agents    []string
	Source    string
}

// PeerBindingStatus defines the observed state of PeerBinding.
type PeerBindingStatus struct {
	CommonStatus
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PeerBindingList is a list of PeerBinding objects.
type PeerBindingList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []PeerBinding
}
