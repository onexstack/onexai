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

	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/aksk"
	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() { registry.Register(&AccessKeyM{}) }

// TableNameAccessKeyM returns the physical table name under the configured
// dialect: agent.access_key on PostgreSQL, onexai_agent_access_key on MySQL.
func TableNameAccessKeyM() string { return dbschema.Table(dbschema.Agent, "access_key") }

// AccessKeyM is one AccessKey/SecretKey pair owned by a platform account.
//
// The owner is the user ID carried in the caller's bearer token. This table
// lives in the agent schema because the credential-management surface is served
// by oai-agent-apiserver; the account itself still lives in chat.user, and the
// agent service never reads that table — it only trusts the claims.
//
// # What is stored
//
// The secret itself is never stored in clear. Two representations serve two
// jobs:
//
//   - SecretDigest is a sha256, the value a future signature check verifies.
//   - SecretCiphertext is sealed with the deployment key so the owner can see
//     the plaintext again from the console. It is empty when the deployment has
//     no encryption key; the console then says the secret cannot be shown,
//     which is the honest answer rather than masking a value that is not there.
//
// Both JSON tags are "-": a row is never serialised straight to a response.
type AccessKeyM struct {
	// ID is the stable identifier the console names a key by. It is unrelated
	// to the credential material: rotating a secret does not change it.
	ID string `gorm:"column:id;primaryKey;size:64" json:"id"`
	// UserID is the owner. Every read and write is narrowed by it.
	UserID string `gorm:"column:user_id;size:64;not null;index:idx_agent_access_key_user" json:"user_id"`
	// Name is the owner's label for the key ("本地开发环境"), not an identity.
	Name string `gorm:"column:name;size:64;not null;default:''" json:"name"`
	// AccessKey is the public half. It is an identifier, safe in clear.
	AccessKey string `gorm:"column:access_key;size:64;uniqueIndex:uq_agent_access_key_access_key" json:"access_key"`
	// SecretDigest is the one-way form used to verify a presented secret.
	SecretDigest string `gorm:"column:secret_digest;size:64" json:"-"`
	// SecretCiphertext is the sealed plaintext, for the reveal path.
	SecretCiphertext string `gorm:"column:secret_ciphertext;type:text" json:"-"`
	// Enabled lets the owner turn a key off without deleting it. A disabled key
	// still counts against the quota, which is what keeps the quota stable.
	Enabled bool `gorm:"column:enabled;not null;default:true" json:"enabled"`
	// LastUsedAt has no writer yet: nothing authenticates with an access key in
	// this deployment. It is nil rather than the creation time dressed up as a
	// use.
	LastUsedAt *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `gorm:"column:created_at;not null;default:current_timestamp" json:"created_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;not null;default:current_timestamp" json:"updated_at"`
}

// TableName returns the physical table name.
func (*AccessKeyM) TableName() string { return TableNameAccessKeyM() }

// NewAccessKey mints a key pair for a user.
//
// The plaintext secret is returned rather than kept on the struct: a field
// holding it would travel with the model into every log line and response that
// serialises the row. The caller seals it and answers with it exactly once.
func NewAccessKey(userID, name string) (*AccessKeyM, string, error) {
	accessKey, secretKey, err := aksk.Generate()
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	return &AccessKeyM{
		ID:           uuid.NewString(),
		UserID:       userID,
		Name:         name,
		AccessKey:    accessKey,
		SecretDigest: aksk.Digest(secretKey),
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, secretKey, nil
}
