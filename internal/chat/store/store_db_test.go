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
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/onexstack/onexai/pkg/dbschema"
)

// newStoreTestDB opens a private in-memory database holding the given models'
// tables and returns the handle the stores under test run on.
//
// The pool is pinned to one connection because an in-memory SQLite database
// belongs to the connection that created it: a second connection would see an
// empty schema.
//
// The dialect is set to MySQL explicitly rather than left to the package
// default. The name Table() returns is dialect-dependent, and another test in
// this package flips the setting to Postgres and back — a Postgres name
// ("chat.peer_agent") is a schema-qualified name SQLite cannot resolve, so
// whichever test ran second would fail for a reason having nothing to do with
// it.
func newStoreTestDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()

	dbschema.Configure(dbschema.MySQL)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		// go-sqlite3 is a stub without cgo. These tests exercise the SQL store,
		// which cannot run without a database; skipping is honest, and CI builds
		// with cgo so they still run there.
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

	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	return db
}
