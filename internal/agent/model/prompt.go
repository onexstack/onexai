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

func init() { registry.Register(&PromptM{}) }

// PromptM is a reusable prompt template referenced by agents via AgentM.PromptRefs.
type PromptM struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string    `gorm:"uniqueIndex;size:128" json:"name"`
	DisplayName string    `gorm:"size:256" json:"display_name"`
	Status      string    `gorm:"size:32" json:"status"`
	Description string    `gorm:"type:text" json:"description"`
	Content     string    `gorm:"type:text" json:"content"` // prompt template content
	DataSources StringMap `gorm:"type:text" json:"data_sources"`
	Namespace   string    `gorm:"column:namespace;size:128;index" json:"namespace"`
	Labels      StringMap `gorm:"-" json:"labels"`
	CreatedAt   int64     `json:"created_at"`
	UpdatedAt   int64     `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the prompt table name.
func (PromptM) TableName() string { return dbschema.Table(dbschema.Agent, "prompt") }

// Enabled reports whether the prompt is active.
func (p PromptM) Enabled() bool { return p.Status != StatusDisabled }

// GetNamespace returns the normalized namespace.
func (p PromptM) GetNamespace() string { return NormalizeNamespace(p.Namespace) }
