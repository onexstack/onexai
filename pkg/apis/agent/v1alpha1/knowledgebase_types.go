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

// KnowledgeBase points at a retrieval dataset on an external platform.
type KnowledgeBase struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec KnowledgeBaseSpec `json:"spec,omitempty"`
	// +optional
	Status KnowledgeBaseStatus `json:"status,omitempty"`
}

// KnowledgeBaseSpec defines the desired state of KnowledgeBase.
type KnowledgeBaseSpec struct {
	CommonSpec `json:",inline"`

	// Platform is dify or ragflow.
	Platform string `json:"platform"`

	// DatasetID is the dataset on that platform.
	DatasetID string `json:"datasetID"`

	// ScoreThreshold drops retrieved chunks below this relevance.
	// +optional
	ScoreThreshold *float64 `json:"scoreThreshold,omitempty"`

	// TopK caps how many chunks one query returns.
	// +optional
	TopK *int32 `json:"topK,omitempty"`

}

// KnowledgeBaseStatus defines the observed state of KnowledgeBase.
type KnowledgeBaseStatus struct {
	CommonStatus `json:",inline"`

	// DocumentCount is what the platform reports.
	// +optional
	DocumentCount int32 `json:"documentCount,omitempty"`

	// LastCheckTime is when reachability was last confirmed.
	// +optional
	LastCheckTime *metav1.Time `json:"lastCheckTime,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// KnowledgeBaseList is a list of KnowledgeBase objects.
type KnowledgeBaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []KnowledgeBase `json:"items"`
}
