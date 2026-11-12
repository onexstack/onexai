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

// Command onexai-mysql2pg copies the oai-agent-apiserver / oai-chat-apiserver /
// oai-jobserver tables from MySQL to PostgreSQL.
//
// It is a one-off cutover tool, not a general-purpose copier: it moves the 22
// known tables verbatim, in both directions of the schema change that came with
// the PostgreSQL switch:
//
//   - Names: on MySQL the tables are onexai_<service>_<name> in one flat
//     schema; on PostgreSQL they are <schema>.<name>, one schema per service
//     (agent / chat / job). See pkg/dbschema.
//   - Types: the logical schema is preserved, but the physical types follow the
//     dialect (longtext -> text, boolean -> boolean, blob -> bytea, datetime(3)
//     -> timestamptz(3), and so on). The target schema is created by the
//     services' own registry.Migrate, so what lands in PostgreSQL is exactly
//     what the applications expect.
//
// Rows are read through the GORM models rather than as raw maps: the models
// carry the type conversions (tinyint(1) -> bool, longtext -> string, blob ->
// []byte), and a map-based copy would hand PostgreSQL an int64 where it wants a
// boolean.
//
// Run the source with a maintenance window: nothing writes to MySQL, but rows
// added mid-copy would be missed.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/onexstack/onexstack/pkg/store/registry"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	// Importing these packages registers their tables with the migration
	// registry; that is what lets registry.Migrate create the target schema.
	agentmodel "github.com/onexstack/onexai/internal/agent/model"
	chatmodel "github.com/onexstack/onexai/internal/chat/model"
	jobmodel "github.com/onexstack/onexai/internal/jobserver/model"
	"github.com/onexstack/onexai/pkg/dbschema"
)

// schemas are the PostgreSQL schemas to create before registry.Migrate runs.
var schemas = []string{"agent", "chat", "job"}

// serialTables are the tables whose primary key is an auto-increment integer.
// The row copy inserts explicit ids, which does not advance the sequence, so
// each one is re-seeded afterwards — otherwise the application's first
// auto-increment insert collides with a copied row.
var serialTables = []string{
	dbschema.Schema(dbschema.Agent) + ".agent",
	dbschema.Schema(dbschema.Agent) + ".skill",
	dbschema.Schema(dbschema.Agent) + ".mcp",
	dbschema.Schema(dbschema.Agent) + ".knowledge_base",
	dbschema.Schema(dbschema.Agent) + ".secret",
	dbschema.Schema(dbschema.Agent) + ".prompt",
	dbschema.Schema(dbschema.Agent) + ".model",
	dbschema.Schema(dbschema.Agent) + ".interaction",
	dbschema.Schema(dbschema.Agent) + ".setting",
	dbschema.Schema(dbschema.Agent) + ".authz_rule",
	dbschema.Schema(dbschema.Agent) + ".k8sgpt_endpoint",
	dbschema.Schema(dbschema.Chat) + ".session",
	dbschema.Schema(dbschema.Chat) + ".ticket",
	dbschema.Schema(dbschema.Chat) + ".chat_message",
	dbschema.Schema(dbschema.Chat) + ".metadata",
	dbschema.Schema(dbschema.Chat) + ".message_record",
	dbschema.Schema(dbschema.Chat) + ".oncall_group",
	dbschema.Schema(dbschema.JobServer) + ".job",
	dbschema.Schema(dbschema.JobServer) + ".cronjob",
}

type options struct {
	srcDSN  string
	dstDSN  string
	batch   int
	dryRun  bool
	timeout time.Duration
}

func main() {
	var opts options
	flag.StringVar(&opts.srcDSN, "src-dsn", "", "source MySQL DSN, e.g. 'user:pass@tcp(host:3306)/zagent?charset=utf8mb4&parseTime=true&loc=Asia%2FShanghai'")
	flag.StringVar(&opts.dstDSN, "dst-dsn", "", "target PostgreSQL DSN in libpq keyword/value form, e.g. 'host=h port=5432 user=u password=p dbname=zagent sslmode=disable'")
	flag.IntVar(&opts.batch, "batch", 200, "rows per INSERT")
	flag.BoolVar(&opts.dryRun, "dry-run", false, "read and report without writing anything (the target schema is still created)")
	flag.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "overall deadline")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if err := run(ctx, opts); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}
}

// moveFn copies one table and returns the number of rows written.
type moveFn func(ctx context.Context) (int64, error)

func run(ctx context.Context, opts options) error {
	if opts.srcDSN == "" || opts.dstDSN == "" {
		return fmt.Errorf("--src-dsn and --dst-dsn are required")
	}

	// The source is read through the same models, with the dialect the MySQL
	// table names belong to. The global flips to postgres inside move() for the
	// write half.
	dbschema.Configure(dbschema.MySQL)

	src, err := openMySQL(ctx, opts.srcDSN)
	if err != nil {
		return err
	}

	dst, err := openPostgres(ctx, opts.dstDSN)
	if err != nil {
		return err
	}

	if err := prepareTarget(ctx, dst); err != nil {
		return err
	}

	moves := []struct {
		label string
		fn    moveFn
	}{
		{"agent.agent", move[agentmodel.AgentM](src, dst, opts, "id")},
		{"agent.skill", move[agentmodel.SkillM](src, dst, opts, "id")},
		{"agent.mcp", move[agentmodel.MCPM](src, dst, opts, "id")},
		{"agent.knowledge_base", move[agentmodel.KnowledgeBaseM](src, dst, opts, "id")},
		{"agent.secret", move[agentmodel.SecretM](src, dst, opts, "id")},
		{"agent.prompt", move[agentmodel.PromptM](src, dst, opts, "id")},
		{"agent.model", move[agentmodel.ModelM](src, dst, opts, "id")},
		{"agent.interaction", move[agentmodel.InteractionM](src, dst, opts, "id")},
		{"agent.authz_rule", move[agentmodel.AuthzRuleM](src, dst, opts, "id")},
		{"agent.k8sgpt_endpoint", move[agentmodel.K8SGPTEndpointM](src, dst, opts, "id")},
		{"chat.session", move[chatmodel.SessionM](src, dst, opts, "id")},
		{"chat.ticket", move[chatmodel.TicketM](src, dst, opts, "id")},
		{"chat.chat_message", move[chatmodel.ChatMessageM](src, dst, opts, "id")},
		{"chat.metadata", move[chatmodel.MetadataM](src, dst, opts, "id")},
		{"chat.oncall_group", move[chatmodel.OncallGroupM](src, dst, opts, "id")},
		{"chat.message_record", move[chatmodel.MessageM](src, dst, opts, "id")},
		{"job.job", move[jobmodel.JobM](src, dst, opts, "id")},
		{"job.cronjob", move[jobmodel.CronJobM](src, dst, opts, "id")},
	}

	var total int64
	for _, m := range moves {
		n, err := m.fn(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", m.label, err)
		}
		total += n
		slog.Info("table copied", "table", m.label, "rows", n)
	}

	if opts.dryRun {
		slog.Info("dry-run: nothing was written except the empty target schema", "rows_read", total)
		return nil
	}

	if err := reseedSequences(ctx, dst); err != nil {
		return err
	}

	slog.Info("migration complete", "rows_written", total)
	return nil
}

// prepareTarget creates the per-service schemas and lets the services' own
// AutoMigrate build the tables, so the resulting schema is by construction the
// one the applications expect.
func prepareTarget(ctx context.Context, dst *gorm.DB) error {
	for _, s := range schemas {
		if err := dst.WithContext(ctx).Exec("CREATE SCHEMA IF NOT EXISTS " + s).Error; err != nil {
			return fmt.Errorf("create schema %s: %w", s, err)
		}
	}

	if err := registry.Migrate(dst); err != nil {
		return fmt.Errorf("auto-migrate target schema: %w", err)
	}
	return nil
}

// move reads every row of one table from MySQL and writes it to PostgreSQL.
//
// The write is an upsert keyed on the primary key, so the tool can be re-run
// after the services are stopped to pick up anything written in the meantime;
// a plain insert would fail on the second run.
//
// The dialect global is flipped between the two halves: the read must see the
// MySQL name (onexai_<service>_<name>) and the write the PostgreSQL one
// (<schema>.<name>). Each *gorm.DB owns its own parsed-schema cache, so the two
// connections never see each other's names.
func move[T any](src, dst *gorm.DB, opts options, conflictColumn string) moveFn {
	return func(ctx context.Context) (int64, error) {
		dbschema.Configure(dbschema.MySQL)

		var rows []T
		if err := src.WithContext(ctx).Find(&rows).Error; err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			return 0, nil
		}
		if opts.dryRun {
			return int64(len(rows)), nil
		}

		dbschema.Configure(dbschema.Postgres)

		// Hooks are skipped deliberately: SessionM.AfterCreate would regenerate
		// ChatID and replace the id the source row carries.
		res := dst.WithContext(ctx).
			Session(&gorm.Session{SkipHooks: true}).
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: conflictColumn}},
				UpdateAll: true,
			}).
			CreateInBatches(&rows, opts.batch)
		if res.Error != nil {
			return 0, res.Error
		}
		return res.RowsAffected, nil
	}
}

// reseedSequences advances each table's id sequence past the copied maximum. A
// NULL pg_get_serial_sequence means the column is not serial-backed, which is
// skipped rather than treated as an error.
func reseedSequences(ctx context.Context, dst *gorm.DB) error {
	for _, table := range serialTables {
		var seq sql.NullString
		if err := dst.WithContext(ctx).
			Raw("SELECT pg_get_serial_sequence(?, 'id')", table).
			Scan(&seq).Error; err != nil {
			return fmt.Errorf("resolve sequence for %s: %w", table, err)
		}
		if !seq.Valid || seq.String == "" {
			slog.Debug("no sequence to reseed", "table", table)
			continue
		}

		sql := fmt.Sprintf(
			"SELECT setval('%s', COALESCE((SELECT MAX(id) FROM %s), 1), (SELECT MAX(id) FROM %s) IS NOT NULL)",
			seq.String, table, table,
		)
		if err := dst.WithContext(ctx).Exec(sql).Error; err != nil {
			return fmt.Errorf("reseed sequence for %s: %w", table, err)
		}
	}
	slog.Info("sequences reseeded", "tables", len(serialTables))
	return nil
}

func openMySQL(ctx context.Context, dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Warn),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connect source mysql: %w", err)
	}
	return db, ping(ctx, db, "source mysql")
}

func openPostgres(ctx context.Context, dsn string) (*gorm.DB, error) {
	// The dialect global must be postgres before the target's schema is parsed;
	// nothing has parsed it yet at this point in the run.
	dbschema.Configure(dbschema.Postgres)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Warn),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connect target postgres: %w", err)
	}
	return db, ping(ctx, db, "target postgres")
}

func ping(ctx context.Context, db *gorm.DB, label string) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("resolve %s pool: %w", label, err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		return fmt.Errorf("ping %s: %w", label, err)
	}
	return nil
}
