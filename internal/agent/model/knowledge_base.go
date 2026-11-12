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

import (
	"strings"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&KnowledgeBaseM{}) }

// KnowledgeBaseM describes an external knowledge base (RAG dataset).
type KnowledgeBaseM struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string `gorm:"uniqueIndex;size:128" json:"name"`
	DisplayName string `gorm:"size:256" json:"display_name"`
	Description string `gorm:"type:text" json:"description"`
	Status      string `gorm:"size:32" json:"status"`
	// Platform names the retrieval backend this record addresses. Values are
	// the Platform* constants below.
	//
	// # Why this is a field rather than a rule about DatasetID
	//
	// Both remaining backends identify their corpus with DatasetID, so the id
	// alone cannot tell Dify from RAGFlow. Naming the platform makes the
	// routing explicit; the failure mode of guessing is silent (the record is
	// simply queried against the wrong platform, or never queried at all).
	//
	// Empty means "not stated", and is resolved by InferPlatform — which keeps
	// the records written before this field existed working. See it for the
	// rule.
	Platform string `gorm:"size:32" json:"platform"`
	// DatasetID is this record's id on the backend named by Platform. For
	// RAGFlow it is the dataset id; for Dify, the dataset id as well.
	DatasetID string `gorm:"size:256" json:"dataset_id"`
	// ScoreThreshold drops passages below this relevance. 0 means the record did
	// not set one, and RAGFlow falls back to agent.ragflow.similarity-threshold.
	ScoreThreshold float64 `json:"score_threshold"`
	// TopK caps how many passages one query returns. 0 means "no record cap";
	// a non-zero value can lower the record's contribution but never raise it
	// above the deployment's global top-k.
	TopK      int    `json:"top_k"`
	Namespace string `gorm:"column:namespace;size:128;index" json:"namespace"`
	Labels            StringMap  `gorm:"-" json:"labels"`
	CreatedAt         int64      `json:"created_at"`
	UpdatedAt         int64      `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the knowledge-base table name.
func (KnowledgeBaseM) TableName() string {
	return dbschema.Table(dbschema.Agent, "knowledge_base")
}

// Enabled reports whether the knowledge base may be queried.
func (k KnowledgeBaseM) Enabled() bool { return k.Status != StatusDisabled }

// GetNamespace returns the normalized namespace.
func (k KnowledgeBaseM) GetNamespace() string { return NormalizeNamespace(k.Namespace) }

// Platform values for KnowledgeBaseM.Platform.
const (
	// PlatformDify is the legacy Dify deployment (KnowledgeBaseOptions). It is
	// also where a record with no stated platform lands, so that rows written
	// before Platform existed keep working.
	PlatformDify = "dify"
	// PlatformRAGFlow is the RAGFlow deployment (RAGFlowOptions).
	PlatformRAGFlow = "ragflow"
)

// InferPlatform resolves the platform this record addresses.
//
// A stated Platform always wins. An empty one means Dify: that is where rows
// written before Platform existed landed, and RAGFlow rows must state their
// platform because DatasetID cannot distinguish the two backends.
func (k KnowledgeBaseM) InferPlatform() string {
	if p := strings.TrimSpace(k.Platform); p != "" {
		return p
	}
	return PlatformDify
}
