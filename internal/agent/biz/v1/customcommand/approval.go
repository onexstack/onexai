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
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
)

const (
	// approvalTTL bounds how long a parked tool call waits for a human. A card
	// whose buttons outlive it still answers, with an explicit "expired" toast.
	approvalTTL = 15 * time.Minute
	// approvalStatusPending is the only phase an approve call may act on.
	approvalStatusPending = "pending"
)

// ErrApprovalNotFound reports an approval id the store does not know.
var ErrApprovalNotFound = errors.New("custom command approval not found")

// ApprovalStore persists parked tool calls.
type ApprovalStore interface {
	Create(ctx context.Context, row *model.CustomCommandApprovalM) error
	Get(ctx context.Context, approvalID string) (*model.CustomCommandApprovalM, error)
	Update(ctx context.Context, row *model.CustomCommandApprovalM) error
}

type sqlApprovalStore struct {
	db *gorm.DB
}

// NewApprovalStore builds the SQL-backed approval store. A nil db yields a
// no-op implementation: without it, a gated tool command reports that approval
// is unavailable instead of failing the whole command surface.
func NewApprovalStore(db *gorm.DB) ApprovalStore {
	if db == nil {
		return noopApprovalStore{}
	}
	return &sqlApprovalStore{db: db}
}

func (s *sqlApprovalStore) Create(ctx context.Context, row *model.CustomCommandApprovalM) error {
	return s.db.WithContext(ctx).Create(row).Error
}

func (s *sqlApprovalStore) Get(
	ctx context.Context, approvalID string,
) (*model.CustomCommandApprovalM, error) {
	var row model.CustomCommandApprovalM
	err := s.db.WithContext(ctx).Where("approval_id = ?", approvalID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrApprovalNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading custom command approval: %w", err)
	}
	return &row, nil
}

func (s *sqlApprovalStore) Update(ctx context.Context, row *model.CustomCommandApprovalM) error {
	if row == nil {
		return nil
	}
	return s.db.WithContext(ctx).Save(row).Error
}

type noopApprovalStore struct{}

func (noopApprovalStore) Create(context.Context, *model.CustomCommandApprovalM) error {
	return errors.New("approval store is not configured")
}

func (noopApprovalStore) Get(context.Context, string) (*model.CustomCommandApprovalM, error) {
	return nil, ErrApprovalNotFound
}

func (noopApprovalStore) Update(context.Context, *model.CustomCommandApprovalM) error {
	return errors.New("approval store is not configured")
}
