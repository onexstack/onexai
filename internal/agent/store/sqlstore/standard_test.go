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
	"errors"
	"strings"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
)

// baseCreatedAt is when a fixture row was created, in milliseconds — the unit
// every timestamp column of these tables is read in.
const baseCreatedAt int64 = 1710000000000

// updatedMark is what an update writes into the column the payload owns.
const updatedMark = "edited"

// newTestStandardStore opens a private in-memory database holding the given
// records' tables and returns a StandardStore over it.
func newTestStandardStore(t *testing.T, recs ...any) *Store {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		// go-sqlite3 is a stub without cgo. The tests in this file exercise the
		// SQL store, which cannot run without a database; skipping is honest,
		// and CI builds with cgo so they still run there.
		if strings.Contains(err.Error(), "requires cgo") || strings.Contains(err.Error(), "stub") {
			t.Skipf("sqlite requires cgo: %v", err)
		}
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap *sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.AutoMigrate(recs...); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	return NewStore(db)
}

// TestStandardSecretLifecycle pins the five-method surface: create, read by
// key, page, replace, delete. It runs against a table that is still SQL-backed —
// the resources that moved to the control plane have their own tests.
func TestStandardSecretLifecycle(t *testing.T) {
	s := newTestStandardStore(t, &model.SecretM{})
	ctx := context.Background()

	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := s.Secrets().Create(ctx, &model.SecretM{SecretID: name, Description: name}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	row, err := s.Secrets().Get(ctx, where.F("secret_id", "beta"))
	if err != nil {
		t.Fatalf("get beta: %v", err)
	}
	if row.SecretID != "beta" {
		t.Fatalf("get beta: got %q", row.SecretID)
	}

	if _, err := s.Secrets().Get(ctx, where.F("secret_id", "missing")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get missing: want store.ErrNotFound, got %v", err)
	}

	total, page, err := s.Secrets().List(ctx, where.NewWhere().O(1).L(1))
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	if total != 3 {
		t.Fatalf("list page: total = %d, want 3", total)
	}
	if len(page) != 1 || page[0].SecretID != "beta" {
		t.Fatalf("list page: got %v", page)
	}

	row, err = s.Secrets().Get(ctx, where.F("secret_id", "beta"))
	if err != nil {
		t.Fatalf("get beta before update: %v", err)
	}
	row.Description = updatedMark
	if err := s.Secrets().Update(ctx, row); err != nil {
		t.Fatalf("update: %v", err)
	}
	row, err = s.Secrets().Get(ctx, where.F("secret_id", "beta"))
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if row.Description != updatedMark {
		t.Fatalf("update did not write description: %q", row.Description)
	}

	if err := s.Secrets().Delete(ctx, where.F("secret_id", "gamma")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Secrets().Get(ctx, where.F("secret_id", "gamma")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get deleted: want store.ErrNotFound, got %v", err)
	}
}

// TestStandardUpdateKeepsRowIdentity pins the contract the generic Update
// relies on: the payload is the row that was read (Get, then change a field),
// so its id and created_at travel with it and come back unchanged.
func TestStandardUpdateKeepsRowIdentity(t *testing.T) {
	s := newTestStandardStore(t, &model.SecretM{})
	ctx := context.Background()

	if err := s.Secrets().Create(ctx, &model.SecretM{
		SecretID: "probe", Description: "original", CreatedAt: baseCreatedAt, UpdatedAt: baseCreatedAt,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before, err := s.Secrets().Get(ctx, where.F("secret_id", "probe"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	update := *before
	update.Description = updatedMark
	if err := s.Secrets().Update(ctx, &update); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, err := s.Secrets().Get(ctx, where.F("secret_id", "probe"))
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("update renumbered the row: %d → %d", before.ID, after.ID)
	}
	if after.CreatedAt != before.CreatedAt {
		t.Fatalf("update backdated created_at: %d → %d", before.CreatedAt, after.CreatedAt)
	}
}
