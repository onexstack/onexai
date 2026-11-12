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
	"fmt"

	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/chat/model"
)

// MetadataStore is the standard store for the resource-metadata table plus the
// JSON-field search the /meta command needs.
type MetadataStore interface {
	Create(ctx context.Context, obj *model.MetadataM) error
	Update(ctx context.Context, obj *model.MetadataM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.MetadataM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.MetadataM, error)

	MetadataExpansion
}

// MetadataExpansion holds the search that reads a JSON field. The expression
// differs per dialect, so it cannot be expressed with the portable where
// options.
type MetadataExpansion interface {
	// Search returns up to limit rows of one domain whose JSON field matches
	// value as a substring.
	Search(ctx context.Context, domain, field, value string, limit int) ([]*model.MetadataM, error)
}

type metadataStore struct {
	*nilSafeStore[model.MetadataM]
	db *gorm.DB
}

var _ MetadataStore = (*metadataStore)(nil)

func newMetadataStore(s *store) *metadataStore {
	return &metadataStore{nilSafeStore: newNilSafeStore[model.MetadataM](s), db: s.db}
}

// Search implements MetadataStore.
//
// MySQL takes a "$.field" path through JSON_EXTRACT, PostgreSQL takes the bare
// key through the ->> operator. Everything else (placeholders, LIKE, LIMIT) is
// portable, so only the expression swaps.
func (s *metadataStore) Search(
	ctx context.Context, domain, field, value string, limit int,
) ([]*model.MetadataM, error) {
	if s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	pattern := "%" + value + "%"
	var (
		sql  string
		args []any
	)
	if s.db.Dialector.Name() == "postgres" {
		sql = fmt.Sprintf(
			"SELECT id, domain, data FROM %s WHERE domain = ? AND data ->> ? LIKE ? LIMIT ?",
			model.TableNameMetadataM(),
		)
		args = []any{domain, field, pattern, limit}
	} else {
		sql = fmt.Sprintf(
			"SELECT id, domain, data FROM %s WHERE domain = ? AND JSON_EXTRACT(data, ?) LIKE ? LIMIT ?",
			model.TableNameMetadataM(),
		)
		args = []any{domain, "$." + field, pattern, limit}
	}
	var rows []*model.MetadataM
	if err := s.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
