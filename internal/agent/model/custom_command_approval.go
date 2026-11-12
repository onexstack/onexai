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

func init() { registry.Register(&CustomCommandApprovalM{}) }

// CustomCommandApprovalM is one parked tool invocation waiting for a human.
//
// The rendered arguments are stored here, not sent back by the approver: the
// execution runs exactly what the card showed, so the approval cannot be
// replayed with different parameters.
type CustomCommandApprovalM struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	ApprovalID     string `gorm:"size:64;uniqueIndex" json:"approval_id"`
	UID            string `gorm:"size:64" json:"uid"`
	Command        string `gorm:"size:128;index" json:"command"`
	Agent          string `gorm:"size:128;index" json:"agent"`
	UserID         string `gorm:"column:user_id;size:128;index" json:"user_id"`
	ConversationID string `gorm:"column:conversation_id;size:128" json:"conversation_id"`
	Tool           string `gorm:"size:256" json:"tool"`
	MCPServer      string `gorm:"column:mcp_server;size:128" json:"mcp_server"`
	Arguments      string `gorm:"type:text" json:"arguments"`
	// Phase is pending, approved or denied.
	Phase     string `gorm:"size:32;index" json:"phase"`
	Reason    string `gorm:"size:256" json:"reason"`
	Result    string `gorm:"type:text" json:"result"`
	CreatedAt int64  `gorm:"index" json:"created_at"`
	ExpiresAt int64  `gorm:"index" json:"expires_at"`
	DecidedAt int64  `json:"decided_at"`
	DecidedBy string `gorm:"size:128" json:"decided_by"`
}

// TableName returns the approval table name.
func (CustomCommandApprovalM) TableName() string {
	return dbschema.Table(dbschema.Agent, "custom_command_approval")
}
