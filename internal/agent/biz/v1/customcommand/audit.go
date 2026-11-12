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

package customcommand

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
)

// AuditQuery filters the invocation history.
type AuditQuery struct {
	Command string
	Agent   string
	UserID  string
	Phase   string
	Offset  int
	Limit   int
}

// AuditStore records and lists command invocations. A deployment without a DB
// gets a nil store; recording then degrades to the structured log and metric.
type AuditStore interface {
	Write(ctx context.Context, row *model.CustomCommandAuditM) error
	List(ctx context.Context, q AuditQuery) (int64, []*model.CustomCommandAuditM, error)
}

type sqlAuditStore struct {
	db *gorm.DB
}

// NewAuditStore builds the SQL-backed audit store. A nil db yields a no-op
// implementation rather than a panic: the command surface still works, and the
// missing history is visible in the service's configuration rather than in a
// failed invocation.
func NewAuditStore(db *gorm.DB) AuditStore {
	if db == nil {
		return noopAuditStore{}
	}
	return &sqlAuditStore{db: db}
}

func (s *sqlAuditStore) Write(ctx context.Context, row *model.CustomCommandAuditM) error {
	if row == nil {
		return nil
	}
	return s.db.WithContext(ctx).Create(row).Error
}

func (s *sqlAuditStore) List(
	ctx context.Context, q AuditQuery,
) (int64, []*model.CustomCommandAuditM, error) {
	tx := s.db.WithContext(ctx).Model(&model.CustomCommandAuditM{})
	if q.Command != "" {
		tx = tx.Where("command = ?", q.Command)
	}
	if q.Agent != "" {
		tx = tx.Where("agent = ?", q.Agent)
	}
	if q.UserID != "" {
		tx = tx.Where("user_id = ?", q.UserID)
	}
	if q.Phase != "" {
		tx = tx.Where("phase = ?", q.Phase)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return 0, nil, fmt.Errorf("counting custom command audits: %w", err)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var rows []*model.CustomCommandAuditM
	if err := tx.Order("id DESC").Offset(q.Offset).Limit(limit).Find(&rows).Error; err != nil {
		return 0, nil, fmt.Errorf("listing custom command audits: %w", err)
	}
	return total, rows, nil
}

type noopAuditStore struct{}

func (noopAuditStore) Write(context.Context, *model.CustomCommandAuditM) error { return nil }
func (noopAuditStore) List(context.Context, AuditQuery) (int64, []*model.CustomCommandAuditM, error) {
	return 0, nil, nil
}
