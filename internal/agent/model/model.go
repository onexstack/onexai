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

func init() { registry.Register(&ModelM{}) }

// ModelM is a named LLM model endpoint configuration.
type ModelM struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// Namespace is the business line that owns the endpoint on the control
	// plane. It is not a SQL column: records migrated from the old table have
	// none and normalize to DefaultNamespace, exactly like every other
	// control-plane-native resource here.
	Namespace   string `gorm:"-" json:"namespace,omitempty"`
	Name        string `gorm:"uniqueIndex;size:128" json:"name"`
	DisplayName string `gorm:"size:256" json:"display_name"`
	Status      string `gorm:"size:32" json:"status"`
	Description string `gorm:"type:text" json:"description"`
	// Owner scopes this endpoint to a subject. Empty means a platform-wide
	// endpoint; a non-empty value is the platform user that configured it
	// (OneX's userID). Per-run model selection refuses an endpoint whose owner
	// is not the run's subject.
	Owner    string    `gorm:"size:64;index" json:"owner"`
	Provider string    `gorm:"size:64" json:"provider"` // "openai" | "deepseek" | ...
	BaseURL  string    `gorm:"size:512" json:"base_url"`
	APIKey   string    `gorm:"size:512" json:"api_key"`
	Model    string    `gorm:"size:128" json:"model"`
	Labels   StringMap `gorm:"-" json:"labels"`
	// Capabilities declares what this endpoint can do beyond text, as the
	// names chatmodel.Capability defines. Empty means "not declared", and
	// every reader falls back to the inference it used before this field
	// existed — see chatmodel.DefaultFor.
	//
	// It is a column of its own rather than a reuse of Labels because the two
	// answer different questions: a label is how a model is found, a capability
	// is what it can be asked to do, and a filter written for one would
	// silently change meaning under the other.
	Capabilities StringList `gorm:"type:text" json:"capabilities"`
	CreatedAt    int64      `json:"created_at"`
	UpdatedAt    int64      `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the model table name.
func (ModelM) TableName() string { return dbschema.Table(dbschema.Agent, "model") }

// Enabled reports whether the model is active.
func (m ModelM) Enabled() bool { return m.Status != StatusDisabled }
