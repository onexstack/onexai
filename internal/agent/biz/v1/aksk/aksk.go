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

// Package aksk implements personal AccessKey/SecretKey management.
//
// The owner is always the user ID carried in the caller's bearer token; there
// is no administrator path that reads another account's keys. The rows live in
// the agent schema (agent.access_key), which is why this surface is served by
// oai-agent-apiserver rather than the chat service.
package aksk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	secretutil "github.com/onexstack/onexstack/pkg/util/secret"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

// MaxKeysPerUser bounds how many credentials one account may hold.
//
// A key is a long-lived credential; ten is far above any legitimate use and
// low enough that a script cannot fill the table. Disabled keys count, so the
// quota does not move when a key is switched off.
const MaxKeysPerUser = 10

// Config carries what the credential service needs beyond its store.
type Config struct {
	// CipherSecret is the deployment secret the secret-key ciphertext is
	// derived from. Empty turns the reveal path off without turning credential
	// creation off.
	CipherSecret string
}

// AccessKeyView is one credential as the console lists it. It never carries
// the secret.
type AccessKeyView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	AccessKey  string     `json:"access_key"`
	Enabled    bool       `json:"enabled"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ListResponse is the owner's key list plus the quota.
type ListResponse struct {
	Keys  []AccessKeyView `json:"keys"`
	Total int64           `json:"total"`
	Limit int             `json:"limit"`
}

// CreateRequest mints a key for the signed-in account.
type CreateRequest struct {
	UserID string
	Name   string
}

// CreateResponse carries the only copy of the plaintext secret.
type CreateResponse struct {
	Key       AccessKeyView `json:"key"`
	SecretKey string        `json:"secret_key"`
}

// AccessKeyBiz is the personal credential surface.
type AccessKeyBiz interface {
	List(ctx context.Context, userID string) (*ListResponse, error)
	Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error)
	// Reveal returns the plaintext secret. An empty string is a real answer:
	// the deployment cannot produce it (no encryption key, or the row predates
	// encryption). The console renders that as "不可查看".
	Reveal(ctx context.Context, userID, id string) (string, error)
	SetEnabled(ctx context.Context, userID, id string, enabled bool) error
	Delete(ctx context.Context, userID, id string) error
}

type accessKeyBiz struct {
	store     agentstore.AIStore
	cipherKey string
}

var _ AccessKeyBiz = (*accessKeyBiz)(nil)

// New creates an AccessKeyBiz.
func New(store agentstore.AIStore, config *Config) *accessKeyBiz {
	if config == nil {
		config = &Config{}
	}
	return &accessKeyBiz{store: store, cipherKey: deriveCipherKey(config.CipherSecret)}
}

func (b *accessKeyBiz) keys() agentstore.AccessKeyStore {
	if b == nil || b.store == nil {
		return nil
	}
	return b.store.AccessKeys()
}

// List implements AccessKeyBiz.
func (b *accessKeyBiz) List(ctx context.Context, userID string) (*ListResponse, error) {
	keys := b.keys()
	if keys == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有凭据存储")
	}
	rows, err := keys.List(ctx, userID)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取凭据列表失败: %s", err.Error())
	}
	out := make([]AccessKeyView, 0, len(rows))
	for _, row := range rows {
		out = append(out, viewOf(row))
	}
	return &ListResponse{Keys: out, Total: int64(len(out)), Limit: MaxKeysPerUser}, nil
}

// Create implements AccessKeyBiz.
//
// The plaintext leaves this method exactly once. What the row keeps is the
// digest (what a future signature check verifies) and the sealed ciphertext
// (what the owner's reveal button opens).
func (b *accessKeyBiz) Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error) {
	keys := b.keys()
	if keys == nil {
		return nil, errno.ErrNotConfigured.WithMessage("本部署没有凭据存储")
	}
	if req.UserID == "" {
		return nil, errno.ErrUnauthenticated
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errno.ErrInvalidArgument.WithMessage("请填写密钥名称")
	}
	if len([]rune(name)) > 32 {
		return nil, errno.ErrInvalidArgument.WithMessage("名称最多 32 个字符")
	}

	total, err := keys.Count(ctx, req.UserID)
	if err != nil {
		return nil, errno.ErrDBRead.WithMessage("读取凭据数量失败: %s", err.Error())
	}
	if total >= MaxKeysPerUser {
		return nil, errno.ErrPermissionDenied.WithMessage(
			"最多只能创建 %d 对凭据，请先删除不再使用的凭据", MaxKeysPerUser)
	}

	row, secret, err := model.NewAccessKey(req.UserID, name)
	if err != nil {
		return nil, errno.ErrInternal.WithMessage("生成凭据失败: %s", err.Error())
	}
	// A deployment key is what makes the reveal button work. Without one the
	// column stays empty and the console says so; the key is still valid.
	if b.cipherKey != "" {
		ciphertext, err := secretutil.Encrypt(secret, secretutil.WithKey(b.cipherKey))
		if err != nil {
			return nil, errno.ErrInternal.WithMessage("加密凭据失败: %s", err.Error())
		}
		row.SecretCiphertext = ciphertext
	}
	if err := keys.Create(ctx, row); err != nil {
		return nil, errno.ErrDBWrite.WithMessage("保存凭据失败: %s", err.Error())
	}
	return &CreateResponse{Key: viewOf(row), SecretKey: secret}, nil
}

// Reveal implements AccessKeyBiz.
func (b *accessKeyBiz) Reveal(ctx context.Context, userID, id string) (string, error) {
	keys := b.keys()
	if keys == nil {
		return "", errno.ErrNotConfigured.WithMessage("本部署没有凭据存储")
	}
	row, err := keys.Get(ctx, userID, id)
	if err != nil {
		return "", errno.ErrNotFound.WithMessage("凭据不存在")
	}
	if row.SecretCiphertext == "" || b.cipherKey == "" {
		return "", nil
	}
	secret, err := secretutil.Decrypt(row.SecretCiphertext, secretutil.WithKey(b.cipherKey))
	if err != nil {
		return "", nil
	}
	return secret, nil
}

// SetEnabled implements AccessKeyBiz.
func (b *accessKeyBiz) SetEnabled(ctx context.Context, userID, id string, enabled bool) error {
	keys := b.keys()
	if keys == nil {
		return errno.ErrNotConfigured.WithMessage("本部署没有凭据存储")
	}
	if err := keys.SetEnabled(ctx, userID, id, enabled); err != nil {
		return errno.ErrNotFound.WithMessage("凭据不存在")
	}
	return nil
}

// Delete implements AccessKeyBiz.
func (b *accessKeyBiz) Delete(ctx context.Context, userID, id string) error {
	keys := b.keys()
	if keys == nil {
		return errno.ErrNotConfigured.WithMessage("本部署没有凭据存储")
	}
	if err := keys.Delete(ctx, userID, id); err != nil {
		return errno.ErrDBWrite.WithMessage("删除凭据失败: %s", err.Error())
	}
	return nil
}

// viewOf maps a row to the console shape.
func viewOf(row *model.AccessKeyM) AccessKeyView {
	return AccessKeyView{
		ID:         row.ID,
		Name:       row.Name,
		AccessKey:  row.AccessKey,
		Enabled:    row.Enabled,
		LastUsedAt: row.LastUsedAt,
		CreatedAt:  row.CreatedAt,
	}
}

// deriveCipherKey turns the deployment's JWT secret into a 32-byte AES key.
//
// The JWT secret is the secret this service already holds, and reusing it
// avoids a second config knob whose absence would silently turn the reveal path
// off. Hashing first accepts a secret of any length (AES requires exactly 16,
// 24 or 32 bytes).
func deriveCipherKey(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:32]
}
