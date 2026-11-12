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

func init() { registry.Register(&A2AInvocationM{}) }

// A2AInvocationM is one outbound A2A call, for debugging and audit.
//
// It is a protocol-level record, not a user-turn record: taskId, contextId,
// card digest and artifacts are what answers "why did this call fail" and
// "which version answered". InteractionM keeps the user-facing turn.
type A2AInvocationM struct {
	ID uint64 `gorm:"primaryKey;autoIncrement" json:"id"`

	InvocationID string `gorm:"uniqueIndex;size:64" json:"invocation_id"`
	// RequestID is the caller's idempotency key. It is unique per agent.
	RequestID string `gorm:"size:128;index" json:"request_id"`

	AgentName string `gorm:"size:128;index" json:"agent_name"`
	Namespace string `gorm:"size:128;index" json:"namespace"`
	AgentKind string `gorm:"size:20" json:"agent_kind"`

	// Source is console, chat, agent-tool or api.
	Source string `gorm:"size:20;index" json:"source"`
	// CallerKind is user or agent.
	CallerKind string `gorm:"size:20" json:"caller_kind"`
	CallerID   string `gorm:"size:128" json:"caller_id"`
	CallerName string `gorm:"size:128" json:"caller_name"`

	CallerAgent string `gorm:"size:128" json:"caller_agent"`
	ParentRunID string `gorm:"size:64;index" json:"parent_run_id"`
	RootRunID   string `gorm:"size:64" json:"root_run_id"`

	TaskID    string `gorm:"size:128;index" json:"task_id"`
	ContextID string `gorm:"size:128" json:"context_id"`

	State     string `gorm:"size:32;index" json:"state"`
	Method    string `gorm:"size:32" json:"method"`
	Transport string `gorm:"size:20" json:"transport"`
	AuthType  string `gorm:"size:20" json:"auth_type"`

	Input        string `gorm:"type:text" json:"input"`
	Output       string `gorm:"type:text" json:"output"`
	ErrorMessage string `gorm:"type:text" json:"error_message"`

	LatencyMs int64 `json:"latency_ms"`

	CardVersion string `gorm:"size:128" json:"card_version"`
	CardDigest  string `gorm:"size:80" json:"card_digest"`

	ArtifactCount int32  `json:"artifact_count"`
	ArtifactsJSON string `gorm:"type:text" json:"artifacts"`
	EventsJSON    string `gorm:"type:text" json:"events"`
	Truncated     bool   `json:"truncated"`

	InteractionID string `gorm:"size:64" json:"interaction_id"`

	CreatedAt   time.Time  `gorm:"not null;default:current_timestamp" json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TableName returns the A2A invocation table name.
func (A2AInvocationM) TableName() string {
	return dbschema.Table(dbschema.Agent, "a2a_invocation")
}
