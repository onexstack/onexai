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

package model

// MemoryM is one long-term memory space. It is read from the control plane;
// entries are stored in the memory_entry table.
type MemoryM struct {
	Name        string `gorm:"-" json:"name"`
	UID         string `gorm:"-" json:"uid"`
	Namespace   string `gorm:"-" json:"namespace"`
	DisplayName string `gorm:"-" json:"display_name"`
	Description string `gorm:"-" json:"description"`
	Status      string `gorm:"-" json:"status"`
	Enabled     bool   `gorm:"-" json:"enabled"`
	Owner       string `gorm:"-" json:"owner"`

	EmbeddingModel      string `gorm:"-" json:"embedding_model"`
	EmbeddingDimensions int32  `gorm:"-" json:"embedding_dimensions"`
	EmbeddingVersion    string `gorm:"-" json:"embedding_version"`

	ExtractionEnabled bool   `gorm:"-" json:"extraction_enabled"`
	ExtractionModel   string `gorm:"-" json:"extraction_model"`
	MaxFactsPerRun    int32  `gorm:"-" json:"max_facts_per_run"`

	TTLDays              int32 `gorm:"-" json:"ttl_days"`
	MaxEntriesPerSubject int32 `gorm:"-" json:"max_entries_per_subject"`

	RetrievalMode string  `gorm:"-" json:"retrieval_mode"`
	TopK          int32   `gorm:"-" json:"top_k"`
	MinScore      float64 `gorm:"-" json:"min_score"`
	MaxTokens     int32   `gorm:"-" json:"max_tokens"`

	PIIRedactionEnabled  bool   `gorm:"-" json:"pii_redaction_enabled"`
	PIIStrategy          string `gorm:"-" json:"pii_strategy"`
	BlockInstructionLike bool   `gorm:"-" json:"block_instruction_like"`
	AllowAdminContentRead bool  `gorm:"-" json:"allow_admin_content_read"`

	SubjectMode string    `gorm:"-" json:"subject_mode"`
	Phase       string    `gorm:"-" json:"phase"`
	Labels      StringMap `gorm:"-" json:"labels"`
	CreatedAt   int64     `gorm:"-" json:"created_at"`
	UpdatedAt   int64     `gorm:"-" json:"updated_at"`
}
