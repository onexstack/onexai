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
	"github.com/onexstack/onexai/pkg/dbschema"
)

// SecretM is a named secret referenced by resources (keyed by SecretID).
//
// It is a view/DTO. The source of truth for an AK/SK credential is a core
// Secret; the deprecated SQL table remains only for the rollback window and is
// no longer registered with AutoMigrate.
type SecretM struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	SecretID    string `gorm:"uniqueIndex;size:128" json:"secret_id"`
	SecretKey   string `gorm:"size:512" json:"secret_key"`
	Description string `gorm:"type:text" json:"description"`
	// Kind separates credentials by who owns them. It is not a column: the
	// control-plane store maps it to the onexai.io/secret-kind label. Empty
	// means an AK/SK credential (the historical default).
	Kind      string `gorm:"-" json:"-"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// Secret kinds understood by the control-plane secret store.
const (
	// SecretKindAKSK is the historical credential kind and the only one the
	// console's credential list shows.
	SecretKindAKSK = "aksk"
	// SecretKindWebhook marks a token the platform stores on behalf of a
	// run-lifecycle Webhook. It is deliberately hidden from the credential
	// list so operators do not rotate it out from under the hook.
	SecretKindWebhook = "webhook"
)

// TableName returns the secret table name.
func (SecretM) TableName() string { return dbschema.Table(dbschema.Agent, "secret") }
