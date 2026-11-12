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
	"time"

	storelogger "github.com/onexstack/onexstack/pkg/logger/slog/store"
	genericstore "github.com/onexstack/onexstack/pkg/store"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// assetStore is the asset table's store. All five standard methods are
// inherited from the generic store; the three asset-specific lookups are the
// table's Expansion.
//
// Update is a full-row Save, so the caller must hand in the row it read (Get,
// then change the writable fields). Saving a payload built from a request body
// alone would carry a zero primary key and be treated as an insert.
type assetStore struct {
	*genericstore.Store[model.AssetM]
	store *Store
}

// Ensure assetStore implements the AssetStore interface.
var _ agentstore.AssetStore = (*assetStore)(nil)

// newAssetStore creates an assetStore backed by s.
func newAssetStore(s *Store) *assetStore {
	return &assetStore{
		Store: genericstore.NewStore[model.AssetM](s, storelogger.NewLogger()),
		store: s,
	}
}

// GetByObjectKey returns the row for a store key, or ErrNotFound.
func (s *assetStore) GetByObjectKey(ctx context.Context, objectKey string) (*model.AssetM, error) {
	var out model.AssetM
	if err := s.store.DB(ctx).WithContext(ctx).Where("object_key = ?", objectKey).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// GetByDigest returns a row whose bytes have this digest and owner, or
// ErrNotFound.
func (s *assetStore) GetByDigest(ctx context.Context, ownerID, sha256 string) (*model.AssetM, error) {
	var out model.AssetM
	if err := s.store.DB(ctx).WithContext(ctx).
		Where("owner_id = ? AND sha256 = ?", ownerID, sha256).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// ListExpired returns up to limit rows whose expiry has passed, oldest first.
func (s *assetStore) ListExpired(
	ctx context.Context, now time.Time, limit int,
) ([]*model.AssetM, error) {
	if limit <= 0 {
		limit = 500
	}
	var rows []*model.AssetM
	// expires_at IS NOT NULL is load-bearing: a NULL expiry means "never", and
	// a comparison against NULL is not true, so the row is excluded either way.
	if err := s.store.DB(ctx).WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at <= ?", now).
		Order("expires_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
