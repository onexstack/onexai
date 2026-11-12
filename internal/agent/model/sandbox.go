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

func init() { registry.Register(&SandboxM{}) }

// Sandbox states.
const (
	SandboxStateRunning = "running"
	SandboxStateStopped = "stopped"
	SandboxStateExpired = "expired"
)

// SandboxM is one owner-scoped execution environment.
type SandboxM struct {
	ID          string `gorm:"primaryKey;size:64" json:"id"`
	Owner       string `gorm:"size:128;index" json:"owner"`
	Template    string `gorm:"size:128" json:"template"`
	Image       string `gorm:"size:1024" json:"image"`
	ContainerID string `gorm:"size:128" json:"container_id"`
	State       string `gorm:"size:32;index" json:"state"`
	ExpiresAt   int64  `gorm:"index" json:"expires_at"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the sandbox table name.
func (SandboxM) TableName() string { return dbschema.Table(dbschema.Agent, "sandbox") }
