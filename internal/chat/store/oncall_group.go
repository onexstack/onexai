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

	storelogger "github.com/onexstack/onexstack/pkg/logger/slog/store"
	genericstore "github.com/onexstack/onexstack/pkg/store"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/chat/model"
)

// OncallGroupStore defines the interface for managing on-call group metadata.
type OncallGroupStore interface {
	// Create inserts a new on-call group.
	Create(ctx context.Context, obj *model.OncallGroupM) error

	// Update saves an existing on-call group.
	Update(ctx context.Context, obj *model.OncallGroupM) error

	// Delete removes on-call groups matching the given criteria.
	Delete(ctx context.Context, opts *where.Options) error

	// Get retrieves a single on-call group matching the given criteria.
	Get(ctx context.Context, opts *where.Options) (*model.OncallGroupM, error)

	// List retrieves on-call groups matching the given criteria together with
	// the total count.
	List(ctx context.Context, opts *where.Options) (int64, []*model.OncallGroupM, error)

	OncallGroupExpansion
}

// OncallGroupExpansion carries the lookups the callers actually need, kept off
// the generic surface so the duty source does not have to build where options.
type OncallGroupExpansion interface {
	// GetByName fetches one group by its unique name (the duty group code).
	GetByName(ctx context.Context, name string) (*model.OncallGroupM, error)
	// ListAll returns every group, for the periodic duty refresh.
	ListAll(ctx context.Context) ([]*model.OncallGroupM, error)
}

// oncallGroupStore implements OncallGroupStore over the generic onexstack store.
type oncallGroupStore struct {
	*genericstore.Store[model.OncallGroupM]
}

var _ OncallGroupStore = (*oncallGroupStore)(nil)

// newOncallGroupStore creates a new oncallGroupStore backed by the given store.
func newOncallGroupStore(s *store) *oncallGroupStore {
	return &oncallGroupStore{
		Store: genericstore.NewStore[model.OncallGroupM](s, storelogger.NewLogger()),
	}
}

// GetByName fetches one group by name.
func (s *oncallGroupStore) GetByName(ctx context.Context, name string) (*model.OncallGroupM, error) {
	return s.Get(ctx, where.F("name", name))
}

// ListAll returns every group.
//
// The options object is a limit of -1 rather than nil: the generic List takes a
// *where.Options, and passing a typed nil makes the store dereference it.
// -1 is GORM's "no limit", which also keeps the generic store's default 100-row
// cap out of a full sweep.
func (s *oncallGroupStore) ListAll(ctx context.Context) ([]*model.OncallGroupM, error) {
	_, groups, err := s.List(ctx, where.L(-1))
	return groups, err
}
