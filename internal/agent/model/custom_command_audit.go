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

func init() { registry.Register(&CustomCommandAuditM{}) }

// CustomCommandAuditM is one user-defined command invocation record.
//
// It deliberately holds no parameters, prompt text or response body: the audit
// answers "who ran what, from where, and how did it end", and anything else
// would copy command payloads into a second store.
type CustomCommandAuditM struct {
	ID              uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	UID             string `gorm:"size:64;index" json:"uid"`
	Command         string `gorm:"size:128;index" json:"command"`
	Agent           string `gorm:"size:128;index" json:"agent"`
	Kind            string `gorm:"size:32" json:"kind"`
	Phase           string `gorm:"size:32;index" json:"phase"`
	Reason          string `gorm:"size:64" json:"reason"`
	UserID          string `gorm:"column:user_id;size:128;index" json:"user_id"`
	ConversationID  string `gorm:"column:conversation_id;size:128" json:"conversation_id"`
	Surface         string `gorm:"size:32" json:"surface"`
	DurationMs      int64  `json:"duration_ms"`
	EvaluationError string `gorm:"type:text" json:"evaluation_error"`
	// CreatedAt is when the invocation finished, in unix millis. It is written
	// by the recorder rather than by GORM so a buffered or delayed write still
	// records when the command ran.
	CreatedAt int64 `gorm:"index" json:"created_at"`
}

// TableName returns the audit table name.
func (CustomCommandAuditM) TableName() string {
	return dbschema.Table(dbschema.Agent, "custom_command_audit")
}
