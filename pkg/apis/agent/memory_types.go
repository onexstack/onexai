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

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Memory is the internal (hub) schema for the memories API.
type Memory struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   MemorySpec
	Status MemoryStatus
}

// MemorySpec defines the desired state of Memory.
type MemorySpec struct {
	CommonSpec

	Embedding         MemoryEmbeddingSpec
	Extraction        MemoryExtractionSpec
	Retention         MemoryRetentionSpec
	Retrieval         MemoryRetrievalSpec
	Security          MemorySecuritySpec
	Scope MemoryScopeSpec
}

// MemoryEmbeddingSpec names the embedding model and vector dimension.
type MemoryEmbeddingSpec struct {
	ModelRef   *Reference
	Dimensions int32
	Version    string
}

// MemoryExtractionSpec configures automatic fact extraction.
type MemoryExtractionSpec struct {
	Enabled        bool
	ModelRef       *Reference
	MaxFactsPerRun int32
}

// MemoryRetentionSpec bounds entry lifetime and count.
type MemoryRetentionSpec struct {
	TTLDays              int32
	MaxEntriesPerSubject int32
}

// MemoryRetrievalSpec configures recall.
type MemoryRetrievalSpec struct {
	Mode      string
	TopK      int32
	MinScore  float64
	MaxTokens int32
}

// MemorySecuritySpec configures content defenses.
type MemorySecuritySpec struct {
	PIIRedaction          PIIRedactionSpec
	BlockInstructionLike  bool
	AllowAdminContentRead bool
}

// PIIRedactionSpec configures PII handling.
type PIIRedactionSpec struct {
	Enabled  bool
	Strategy string
}

// MemoryScopeSpec selects the bucket an entry is written to.
type MemoryScopeSpec struct {
	SubjectMode string
}

// MemoryStatus defines the observed state of Memory.
type MemoryStatus struct {
	CommonStatus

	Phase string
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemoryList is a list of Memory objects.
type MemoryList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []Memory
}
