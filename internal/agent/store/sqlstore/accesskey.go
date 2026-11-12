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

package sqlstore

import (
	"context"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// accessKeyStore implements the personal credential table.
//
// The operations are hand-written rather than inherited from the generic store
// because every one of them carries the owner in its predicate. A generic
// Get/Delete takes a where.Options a caller could build without user_id, which
// is exactly the mistake this table must not allow.
type accessKeyStore struct {
	s *Store
}

var _ agentstore.AccessKeyStore = (*accessKeyStore)(nil)

func newAccessKeyStore(s *Store) *accessKeyStore { return &accessKeyStore{s: s} }

func (a *accessKeyStore) db(ctx context.Context) *gorm.DB {
	if a == nil || a.s == nil {
		return nil
	}
	db := a.s.DB(ctx)
	if db == nil {
		return nil
	}
	return db.WithContext(ctx)
}

// Create implements AccessKeyStore.
func (a *accessKeyStore) Create(ctx context.Context, key *model.AccessKeyM) error {
	db := a.db(ctx)
	if db == nil {
		return agentstore.ErrNotFound
	}
	return db.Create(key).Error
}

// Get implements AccessKeyStore.
func (a *accessKeyStore) Get(ctx context.Context, userID, id string) (*model.AccessKeyM, error) {
	db := a.db(ctx)
	if db == nil || userID == "" || id == "" {
		return nil, agentstore.ErrNotFound
	}
	var out model.AccessKeyM
	if err := db.Where("user_id = ? AND id = ?", userID, id).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// List implements AccessKeyStore.
func (a *accessKeyStore) List(ctx context.Context, userID string) ([]*model.AccessKeyM, error) {
	db := a.db(ctx)
	if db == nil || userID == "" {
		return nil, nil
	}
	var out []*model.AccessKeyM
	err := db.Where("user_id = ?", userID).Order("created_at DESC").Find(&out).Error
	return out, err
}

// Count implements AccessKeyStore.
func (a *accessKeyStore) Count(ctx context.Context, userID string) (int64, error) {
	db := a.db(ctx)
	if db == nil || userID == "" {
		return 0, nil
	}
	var total int64
	err := db.Model(&model.AccessKeyM{}).Where("user_id = ?", userID).Count(&total).Error
	return total, err
}

// SetEnabled implements AccessKeyStore.
func (a *accessKeyStore) SetEnabled(ctx context.Context, userID, id string, enabled bool) error {
	db := a.db(ctx)
	if db == nil || userID == "" || id == "" {
		return agentstore.ErrNotFound
	}
	result := db.Model(&model.AccessKeyM{}).
		Where("user_id = ? AND id = ?", userID, id).
		Update("enabled", enabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return agentstore.ErrNotFound
	}
	return nil
}

// Delete implements AccessKeyStore.
func (a *accessKeyStore) Delete(ctx context.Context, userID, id string) error {
	db := a.db(ctx)
	if db == nil || userID == "" || id == "" {
		return nil
	}
	return db.Where("user_id = ? AND id = ?", userID, id).Delete(&model.AccessKeyM{}).Error
}
