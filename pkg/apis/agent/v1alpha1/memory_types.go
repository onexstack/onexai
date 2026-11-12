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

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Memory declares a long-term memory space: how entries are embedded, extracted,
// retained and retrieved. Entries themselves live in the agent SQL store, not in
// etcd; this object is the policy and scope.
type Memory struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec MemorySpec `json:"spec,omitempty"`
	// +optional
	Status MemoryStatus `json:"status,omitempty"`
}

// MemorySpec defines the desired state of Memory.
type MemorySpec struct {
	CommonSpec `json:",inline"`

	// Embedding names the model and the physical vector dimension.
	Embedding MemoryEmbeddingSpec `json:"embedding,omitempty"`

	// Extraction configures run-end fact extraction.
	// +optional
	Extraction MemoryExtractionSpec `json:"extraction,omitempty"`

	// Retention bounds how long and how many entries are kept.
	// +optional
	Retention MemoryRetentionSpec `json:"retention,omitempty"`

	// Retrieval configures how memories are recalled.
	// +optional
	Retrieval MemoryRetrievalSpec `json:"retrieval,omitempty"`

	// Security configures PII redaction and prompt-injection defenses.
	// +optional
	Security MemorySecuritySpec `json:"security,omitempty"`

	// Scope selects the bucket an entry is written to.
	// +optional
	Scope MemoryScopeSpec `json:"scope,omitempty"`

}

// MemoryEmbeddingSpec names the embedding model and vector dimension.
type MemoryEmbeddingSpec struct {
	// ModelRef names a Model in the same namespace. The model must expose an
	// OpenAI-compatible /embeddings endpoint.
	// +optional
	ModelRef *Reference `json:"modelRef,omitempty"`

	// Dimensions is fixed for the physical vector column and index.
	// +optional
	Dimensions int32 `json:"dimensions,omitempty"`

	// Version is a caller-managed generation string. Changing it starts a
	// re-embedding migration.
	// +optional
	Version string `json:"version,omitempty"`
}

// MemoryExtractionSpec configures automatic fact extraction.
type MemoryExtractionSpec struct {
	// Enabled turns run-end extraction on.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// ModelRef names the extraction LLM. Empty falls back to the runtime
	// summarizer.
	// +optional
	ModelRef *Reference `json:"modelRef,omitempty"`

	// MaxFactsPerRun caps how many facts one run may add. Zero uses the
	// deployment default.
	// +optional
	MaxFactsPerRun int32 `json:"maxFactsPerRun,omitempty"`
}

// MemoryRetentionSpec bounds entry lifetime and count.
type MemoryRetentionSpec struct {
	// TTLDays is how long an entry lives. Zero means no expiry.
	// +optional
	TTLDays int32 `json:"ttlDays,omitempty"`

	// MaxEntriesPerSubject caps one subject's entries. Zero means no cap.
	// +optional
	MaxEntriesPerSubject int32 `json:"maxEntriesPerSubject,omitempty"`
}

// MemoryRetrievalSpec configures recall.
type MemoryRetrievalSpec struct {
	// Mode is hybrid, vector or keyword. Empty means hybrid.
	// +optional
	Mode string `json:"mode,omitempty"`

	// TopK caps how many entries one recall returns.
	// +optional
	TopK int32 `json:"topK,omitempty"`

	// MinScore drops results below this final score.
	// +optional
	MinScore float64 `json:"minScore,omitempty"`

	// MaxTokens bounds the recalled context.
	// +optional
	MaxTokens int32 `json:"maxTokens,omitempty"`
}

// MemorySecuritySpec configures content defenses.
type MemorySecuritySpec struct {
	// PIIRedaction redacts sensitive values before storage.
	// +optional
	PIIRedaction PIIRedactionSpec `json:"piiRedaction,omitempty"`

	// BlockInstructionLike rejects content that looks like prompt or tool
	// instructions.
	// +optional
	BlockInstructionLike bool `json:"blockInstructionLike,omitempty"`

	// AllowAdminContentRead lets platform administrators read entry content.
	// It is off by default because reading a person's memories is a break-glass
	// operation and is audited.
	// +optional
	AllowAdminContentRead bool `json:"allowAdminContentRead,omitempty"`
}

// PIIRedactionSpec configures PII handling.
type PIIRedactionSpec struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Strategy is redact, hash or drop. Empty means redact.
	// +optional
	Strategy string `json:"strategy,omitempty"`
}

// MemoryScopeSpec selects the bucket an entry is written to.
type MemoryScopeSpec struct {
	// SubjectMode is user or session. Empty means user.
	// +optional
	SubjectMode string `json:"subjectMode,omitempty"`
}

// MemoryStatus defines the observed state of Memory.
type MemoryStatus struct {
	CommonStatus `json:",inline"`

	// Phase is Ready, Degraded or Error.
	// +optional
	Phase string `json:"phase,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemoryList is a list of Memory objects.
type MemoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Memory `json:"items"`
}
