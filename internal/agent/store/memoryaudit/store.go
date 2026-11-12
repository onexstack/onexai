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

// Package memoryaudit persists the append-only memory audit trail.
package memoryaudit

import (
	"context"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
)

// Filter narrows an audit listing.
type Filter struct {
	Namespace string
	Memory    string
	Subject   string
	Action    string
	Offset    int
	Limit     int
}

// Store is the audit surface.
type Store struct {
	db *gorm.DB
}

// New returns an audit store over db.
func New(db *gorm.DB) *Store { return &Store{db: db} }

// Create appends one event.
func (s *Store) Create(ctx context.Context, event *model.MemoryAuditM) error {
	return s.db.WithContext(ctx).Create(event).Error
}

// List returns one page and the total behind it.
func (s *Store) List(ctx context.Context, f Filter) (int64, []*model.MemoryAuditM, error) {
	query := s.db.WithContext(ctx).Model(&model.MemoryAuditM{})
	if f.Namespace != "" {
		query = query.Where("namespace = ?", f.Namespace)
	}
	if f.Memory != "" {
		query = query.Where("memory = ?", f.Memory)
	}
	if f.Subject != "" {
		query = query.Where("subject = ?", f.Subject)
	}
	if f.Action != "" {
		query = query.Where("action = ?", f.Action)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, nil, err
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	var rows []*model.MemoryAuditM
	return total, rows, query.Order("created_at DESC").Offset(f.Offset).Limit(f.Limit).Find(&rows).Error
}
