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

	storelogger "github.com/onexstack/onexstack/pkg/logger/slog/store"
	genericstore "github.com/onexstack/onexstack/pkg/store"
	"gorm.io/gorm/clause"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
)

// namespaceStore is the business-line table's store. The five standard methods
// are inherited from the generic store; Upsert is the one GORM's standard store
// does not offer and the reverse sync needs.
type namespaceStore struct {
	*genericstore.Store[model.NamespaceM]
	store *Store
}

// Ensure namespaceStore implements the NamespaceStore interface.
var _ agentstore.NamespaceStore = (*namespaceStore)(nil)

// newNamespaceStore creates a namespaceStore backed by s.
func newNamespaceStore(s *Store) *namespaceStore {
	return &namespaceStore{
		Store: genericstore.NewStore[model.NamespaceM](s, storelogger.NewLogger()),
		store: s,
	}
}

// Upsert writes a business line, inserting it when the namespace is new.
//
// The conflict target is the namespace name, and only the fields the control
// plane owns are updated: the row id and the creation time are left as they
// were, so a resync cannot renumber or redate a row the console already shows.
func (s *namespaceStore) Upsert(ctx context.Context, obj *model.NamespaceM) error {
	return s.store.DB(ctx).WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"display_name", "description", "owner", "labels", "updated_at",
		}),
	}).Create(obj).Error
}
