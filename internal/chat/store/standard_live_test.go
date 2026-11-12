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
	"os"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/onexstack/onexai/pkg/dbschema"
)

// TestStandardSurfaceAgainstPostgres runs every standard accessor against the
// real schema. It is skipped unless CHAT_STORE_TEST_PG_DSN is set: the point is
// to catch a column/ordering mistake (peer_bind_claim has no id column, the
// MySQL and PostgreSQL table names differ) that SQLite cannot prove.
//
// It only reads. Nothing is created, updated or deleted.
func TestStandardSurfaceAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("CHAT_STORE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set CHAT_STORE_TEST_PG_DSN to run the PostgreSQL integration test")
	}
	dbschema.Configure(dbschema.Postgres)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The unexported concrete type, not NewStore: the package singleton would
	// hand back whichever database another test constructed first.
	s := &store{db: db}
	ctx := context.Background()
	opts := where.L(1)

	if _, err := s.Metadata().Search(ctx, "rds", "name", "", 5); err != nil {
		t.Errorf("metadata Search: %v", err)
	}

	checks := map[string]func() error{
		"sessions": func() error {
			_, _, err := s.Session().List(ctx, opts)
			return err
		},
		"tickets": func() error {
			_, _, err := s.Ticket().List(ctx, opts)
			return err
		},
		"ticket notes": func() error {
			_, _, err := s.TicketNote().List(ctx, opts)
			return err
		},
		"peer agents": func() error {
			_, _, err := s.PeerAgents().List(ctx, opts)
			return err
		},
		"peer claims": func() error {
			_, _, err := s.PeerBindClaims().List(ctx, opts)
			return err
		},
		"chat messages": func() error {
			_, _, err := s.ChatMessages().List(ctx, opts)
			return err
		},
		"messages": func() error {
			_, _, err := s.Messages().List(ctx, opts)
			return err
		},
		"oncall groups": func() error {
			_, _, err := s.OncallGroups().List(ctx, opts)
			return err
		},
		"oncall schedules": func() error {
			_, _, err := s.OncallSchedules().List(ctx, opts)
			return err
		},
		"ticket escalation": func() error {
			_, _, err := s.TicketEscalation().List(ctx, opts)
			return err
		},
		"agent commands": func() error {
			return err
		},
		"user resolve": func() error {
			_, users, err := s.Users().List(ctx, opts)
			if err != nil || len(users) == 0 {
				return err
			}
			// The resolver runs LOWER() against username and both branches of
			// the email query; SQLite cannot prove those are valid Postgres.
			resolved, _, err := s.Users().ResolveByName(ctx, users[0].Username)
			if err != nil {
				return err
			}
			if resolved == nil {
				return fmt.Errorf("ResolveByName(%q) found no account", users[0].Username)
			}
			return nil
		},
	}
	for name, check := range checks {
		if err := check(); err != nil {
			t.Errorf("%s List: %v", name, err)
		}
	}
}
