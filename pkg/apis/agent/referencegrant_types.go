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

package agent

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReferenceGrant is the internal (hub) schema for the referencegrants API.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ReferenceGrant struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec ReferenceGrantSpec
}

// ReferenceGrantSpec describes who may reference what in the grant's namespace.
type ReferenceGrantSpec struct {
	From []ReferenceGrantFrom
	To   []ReferenceGrantTo
}

// ReferenceGrantFrom identifies the referrer side.
type ReferenceGrantFrom struct {
	Group     string
	Kind      string
	Namespace string
}

// ReferenceGrantTo identifies the target side. Name empty means every object
// of Kind in the grant's namespace.
type ReferenceGrantTo struct {
	Group string
	Kind  string
	Name  string
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ReferenceGrantList is a list of ReferenceGrant objects.
type ReferenceGrantList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []ReferenceGrant
}
