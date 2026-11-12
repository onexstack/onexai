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

package store

import (
	"context"
	"time"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
)

// AssetStore persists uploaded asset rows.
type AssetStore interface {
	Create(ctx context.Context, obj *model.AssetM) error
	Update(ctx context.Context, obj *model.AssetM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.AssetM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.AssetM, error)

	AssetExpansion
}

// AssetExpansion holds the asset-specific lookups that are not the standard
// five.
type AssetExpansion interface {
	// GetByObjectKey returns the row for a store key, or ErrNotFound.
	GetByObjectKey(ctx context.Context, objectKey string) (*model.AssetM, error)
	// GetByDigest returns a row whose bytes have this digest and owner, or
	// ErrNotFound.
	GetByDigest(ctx context.Context, ownerID, sha256 string) (*model.AssetM, error)
	// ListExpired returns up to limit rows whose expiry has passed, oldest
	// first.
	ListExpired(ctx context.Context, now time.Time, limit int) ([]*model.AssetM, error)
}
