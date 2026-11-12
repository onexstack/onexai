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
	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&MemoryEntryM{}) }

// MemoryEntryM is one stored long-term memory. The embedding is a portable
// float32 blob so the same code works on SQLite, MySQL and PostgreSQL.
type MemoryEntryM struct {
	ID        string `gorm:"primaryKey;size:36" json:"id"`
	Namespace string `gorm:"size:63;not null;index:idx_memory_scope,priority:1" json:"namespace"`
	Memory    string `gorm:"size:253;not null;index:idx_memory_scope,priority:2" json:"memory"`
	MemoryUID string `gorm:"size:36;not null" json:"memory_uid"`

	Agent string `gorm:"size:128;index" json:"agent"`
	// AgentNamespace is where the writing agent lives. It is not Namespace
	// above: a shared Memory is written in place, so two namespaces may hold an
	// agent with the same name and this is what tells them apart.
	AgentNamespace string `gorm:"size:63;index" json:"agent_namespace"`
	Subject        string `gorm:"size:128;not null;index:idx_memory_subject,priority:1" json:"subject"`
	SessionID      string `gorm:"size:128;index" json:"session_id"`
	RunID          string `gorm:"size:128;index" json:"run_id"`

	Type   string `gorm:"size:32;not null" json:"type"`
	Status string `gorm:"size:32;not null;index" json:"status"`

	Content   string `gorm:"type:text;not null" json:"content"`
	DedupeKey string `gorm:"size:64;not null" json:"dedupe_key"`

	Embedding         []byte `gorm:"column:embedding" json:"-"`
	EmbeddingModel    string `gorm:"size:128;not null;default:''" json:"embedding_model"`
	EmbeddingVersion  string `gorm:"size:64;not null;default:''" json:"embedding_version"`
	EmbeddingStatus   string `gorm:"size:16;not null;default:'pending'" json:"embedding_status"`
	EmbeddingAttempts int    `gorm:"not null;default:0" json:"embedding_attempts"`
	EmbeddingError    string `gorm:"type:text" json:"embedding_error"`
	NextAttemptAt     int64  `gorm:"not null;default:0;index" json:"next_attempt_at"`

	Source string `gorm:"size:16;not null" json:"source"`
	Actor  string `gorm:"size:128" json:"actor"`

	Importance float32 `gorm:"not null;default:0.5" json:"importance"`
	Confidence float32 `gorm:"not null;default:0.5" json:"confidence"`

	AccessCount    int64 `gorm:"not null;default:0" json:"access_count"`
	LastAccessedAt int64 `gorm:"not null;default:0" json:"last_accessed_at"`
	ExpiresAt      int64 `gorm:"not null;default:0;index" json:"expires_at"`

	Tags     string `gorm:"type:text" json:"tags"`
	Metadata string `gorm:"type:text" json:"metadata"`

	Version   int64 `gorm:"not null;default:1" json:"version"`
	CreatedAt int64 `gorm:"not null;autoCreateTime:false" json:"created_at"`
	UpdatedAt int64 `gorm:"not null;autoUpdateTime:false" json:"updated_at"`
	DeletedAt int64 `gorm:"not null;default:0;index" json:"deleted_at"`
}

// TableName returns the memory entry table name.
func (MemoryEntryM) TableName() string { return dbschema.Table(dbschema.Agent, "memory_entry") }
