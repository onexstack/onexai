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

import "github.com/onexstack/onexai/pkg/dbschema"

// AgentTemplateM is a declarative, reusable behavior unit.
//
// It is read from the control plane and never stored in a table: a template is
// a deployment input, like a RuntimeProfile or a SandboxTemplate, not runtime
// state. Spec keeps the whole AgentTemplateSpec as JSON rather than a column
// per field: the list of behavior fields is expected to grow, and a REST
// round-trip must not lose the ones the console does not model yet.
type AgentTemplateM struct {
	Name        string `gorm:"-" json:"name"`
	DisplayName string `gorm:"-" json:"display_name"`
	Description string `gorm:"-" json:"description"`
	Status      string `gorm:"-" json:"status"`
	// Spec is the JSON-encoded AgentTemplateSpec.
	Spec      string    `gorm:"-" json:"spec,omitempty"`
	Namespace string    `gorm:"column:namespace;size:128;index" json:"namespace"`
	Labels    StringMap `gorm:"-" json:"labels,omitempty"`
	CreatedAt int64     `gorm:"-" json:"created_at"`
	UpdatedAt int64     `gorm:"-" json:"updated_at"`
}

// TableName satisfies the registry's naming contract for a row that has no
// table; it is never used to write.
func (AgentTemplateM) TableName() string { return dbschema.Table(dbschema.Agent, "agent_template") }
