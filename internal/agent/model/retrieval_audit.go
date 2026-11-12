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
	"time"

	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&RetrievalAuditM{}) }

// RetrievalAuditM is one knowledge-base search: who asked, which bases and
// documents answered, and how much text was handed over.
//
// It exists for the question a rate limit cannot answer after the fact — "this
// account copied which documents, and when" — which is what a leak
// investigation starts from. It is deliberately append-only and never read by a
// request path: a table on the hot path that a request also reads is a table
// whose failure changes an answer.
type RetrievalAuditM struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// UserID is the run's subject. Empty for a service-account caller, which
	// has no person to attribute a retrieval to.
	UserID string `gorm:"column:user_id;size:64;index" json:"user_id"`
	// Agent is the agent set that ran, first name only.
	Agent string `gorm:"column:agent;size:128;index" json:"agent"`
	// KnowledgeBases is the comma-separated list of bases that answered.
	KnowledgeBases string `gorm:"column:knowledge_bases;type:text" json:"knowledge_bases"`
	// Documents is the number of distinct sources in the answer.
	Documents int `gorm:"column:documents" json:"documents"`
	// Passages is the number of passages actually rendered to the model.
	Passages int `gorm:"column:passages" json:"passages"`
	// Bytes is the UTF-8 byte count of the rendered passages.
	Bytes     int       `gorm:"column:bytes" json:"bytes"`
	TraceID   string    `gorm:"column:trace_id;size:64;index" json:"trace_id"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
}

// TableName returns the audit table name.
func (RetrievalAuditM) TableName() string { return dbschema.Table(dbschema.Agent, "retrieval_audit") }
