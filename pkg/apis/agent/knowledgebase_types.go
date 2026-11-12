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

package agent

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// KnowledgeBase is the internal (hub) schema for the knowledgebases API.
type KnowledgeBase struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   KnowledgeBaseSpec
	Status KnowledgeBaseStatus
}

// KnowledgeBaseSpec defines the desired state of KnowledgeBase.
type KnowledgeBaseSpec struct {
	CommonSpec

	Platform       string
	DatasetID      string
	ScoreThreshold *float64
	TopK           *int32
}

// KnowledgeBaseStatus defines the observed state of KnowledgeBase.
type KnowledgeBaseStatus struct {
	CommonStatus

	DocumentCount int32
	LastCheckTime *metav1.Time
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// KnowledgeBaseList is a list of KnowledgeBase objects.
type KnowledgeBaseList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []KnowledgeBase
}
