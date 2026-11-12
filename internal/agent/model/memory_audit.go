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

func init() { registry.Register(&MemoryAuditM{}) }

// MemoryAuditM is one append-only memory mutation. Content is deliberately not
// stored: the audit answers who changed what, not what the text was.
type MemoryAuditM struct {
	ID        string `gorm:"primaryKey;size:36" json:"id"`
	Namespace string `gorm:"size:63;not null;index" json:"namespace"`
	Memory    string `gorm:"size:253;not null;index" json:"memory"`
	MemoryUID string `gorm:"size:36;not null" json:"memory_uid"`
	Subject   string `gorm:"size:128;not null;index" json:"subject"`
	EntryID   string `gorm:"size:36;index" json:"entry_id"`
	Actor     string `gorm:"size:128" json:"actor"`
	// Action is create, update, delete, dedupe or purge.
	Action string `gorm:"size:16;not null;index" json:"action"`
	Source string `gorm:"size:16" json:"source"`
	Reason string `gorm:"type:text" json:"reason"`
	// CreatedAt is Unix milliseconds, written by the service so it cannot be
	// confused with GORM's Unix-second auto timestamps.
	CreatedAt int64 `gorm:"not null;autoCreateTime:false;index" json:"created_at"`
}

// TableName returns the audit table name.
func (MemoryAuditM) TableName() string { return dbschema.Table(dbschema.Agent, "memory_audit") }
