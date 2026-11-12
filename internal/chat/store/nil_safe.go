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
	"gorm.io/gorm"
)

// nilSafeStore is the standard five-method generic store with the service's
// long-standing "no database, no-op" behaviour.
//
// The deployment can run without a database (tests and a DB-less chat
// deployment), and every sub-store used to check s.db itself. The embedded
// generic store would dereference a nil handle, so the checks live here once
// rather than in six copies.
type nilSafeStore[T any] struct {
	*genericstore.Store[T]
	db *gorm.DB
}

func newNilSafeStore[T any](s *store) *nilSafeStore[T] {
	return &nilSafeStore[T]{
		Store: genericstore.NewStore[T](s, storelogger.NewLogger()),
		db:    s.db,
	}
}

func (s *nilSafeStore[T]) Create(ctx context.Context, obj *T) error {
	if s.db == nil {
		return nil
	}
	return s.Store.Create(ctx, obj)
}

func (s *nilSafeStore[T]) Update(ctx context.Context, obj *T) error {
	if s.db == nil {
		return nil
	}
	return s.Store.Update(ctx, obj)
}

func (s *nilSafeStore[T]) Delete(ctx context.Context, opts *where.Options) error {
	if s.db == nil {
		return nil
	}
	return s.Store.Delete(ctx, opts)
}

func (s *nilSafeStore[T]) Get(ctx context.Context, opts *where.Options) (*T, error) {
	if s.db == nil {
		return nil, ErrRecordNotFound
	}
	return s.Store.Get(ctx, opts)
}

func (s *nilSafeStore[T]) List(ctx context.Context, opts *where.Options) (int64, []*T, error) {
	if s.db == nil {
		return 0, nil, nil
	}
	return s.Store.List(ctx, opts)
}
