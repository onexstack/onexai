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

// Package sandboxstore persists sandbox instances.
package sandboxstore

import (
	"context"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
)

// Store is the sandbox surface.
type Store interface {
	Create(ctx context.Context, sandbox *model.SandboxM) error
	Get(ctx context.Context, id string) (*model.SandboxM, error)
	Update(ctx context.Context, sandbox *model.SandboxM) error
	ListExpired(ctx context.Context, now int64, limit int) ([]*model.SandboxM, error)
	List(ctx context.Context, owner string, limit int) ([]*model.SandboxM, error)
}

// SQLStore is the GORM-backed Store.
type SQLStore struct {
	db *gorm.DB
}

var _ Store = (*SQLStore)(nil)

// New returns a store over db.
func New(db *gorm.DB) *SQLStore { return &SQLStore{db: db} }

func (s *SQLStore) Create(ctx context.Context, sandbox *model.SandboxM) error {
	return s.db.WithContext(ctx).Create(sandbox).Error
}

func (s *SQLStore) Get(ctx context.Context, id string) (*model.SandboxM, error) {
	var sandbox model.SandboxM
	if err := s.db.WithContext(ctx).Where("id = ?", id).Take(&sandbox).Error; err != nil {
		return nil, err
	}
	return &sandbox, nil
}

func (s *SQLStore) Update(ctx context.Context, sandbox *model.SandboxM) error {
	return s.db.WithContext(ctx).Model(&model.SandboxM{}).
		Where("id = ?", sandbox.ID).
		Updates(map[string]any{
			"state":        sandbox.State,
			"container_id": sandbox.ContainerID,
			"expires_at":   sandbox.ExpiresAt,
		}).Error
}

func (s *SQLStore) List(ctx context.Context, owner string, limit int) ([]*model.SandboxM, error) {
	db := s.db.WithContext(ctx).Where("owner = ?", owner).Order("created_at DESC")
	if limit > 0 {
		db = db.Limit(limit)
	}
	var sandboxes []*model.SandboxM
	return sandboxes, db.Find(&sandboxes).Error
}

func (s *SQLStore) ListExpired(ctx context.Context, now int64, limit int) ([]*model.SandboxM, error) {
	if limit <= 0 {
		limit = 100
	}
	var sandboxes []*model.SandboxM
	return sandboxes, s.db.WithContext(ctx).
		Where("state = ? AND expires_at > 0 AND expires_at <= ?", model.SandboxStateRunning, now).
		Limit(limit).Find(&sandboxes).Error
}
