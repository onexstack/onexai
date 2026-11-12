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

// Command identity-backfill rewrites the stored Lark open_ids into user_ids.
//
// onexai used to key a person by whichever of their two Lark ids happened to be
// at hand — the open_id in some columns and the user_id in others — and the two
// are not interchangeable: an open_id is unique per application, so the same
// person had a different one in every app, and none of them could be written
// into a config file. Everything now keys on the tenant user_id (see
// internal/chat/im and pkg/api/chat/v1 for the contract), which
// leaves the rows written before that change pointing at the wrong id.
//
// This tool is the one-off that moves them: it finds every open_id left in the
// columns that hold a person, asks Lark's contact API for the user_id behind
// each one, and rewrites the column. It is idempotent — a second run finds
// nothing to do — and it reports, rather than guesses, the ids it cannot resolve
// (a departed employee, or an app without the permission to read user IDs).
//
// See README.md for the flags and the column list.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/onexstack/onexai/internal/pkg/larkkit"
	"github.com/onexstack/onexai/pkg/dbschema"
)

type options struct {
	dsn          string
	appID        string
	appSecret    string
	tables       string
	apply        bool
	limit        int
	resolveSleep time.Duration
}

func main() {
	var (
		opts options
	)

	flag.StringVar(&opts.dsn, "onex-mysql-dsn", "", "onexai MySQL DSN (required), e.g. 'user:pass@tcp(host:3306)/onex?parseTime=true'")
	flag.StringVar(&opts.appID, "lark-app-id", "", "Lark app id used to resolve user_ids (required)")
	flag.StringVar(&opts.appSecret, "lark-app-secret", "", "Lark app secret used to resolve user_ids (required)")
	flag.StringVar(&opts.tables, "tables", "all", "comma-separated subset of the columns to rewrite, e.g. 'onexai_chat_chat_message.sender_id'; 'all' means every column")
	flag.BoolVar(&opts.apply, "apply", false, "write the rewrites; without it the tool only reports what it would do")
	flag.IntVar(&opts.limit, "limit", 0, "stop after this many rows per column (0 = no limit); meant for a first cautious run")
	flag.DurationVar(&opts.resolveSleep, "resolve-sleep", 120*time.Millisecond, "pause between contact API calls, to stay under the rate limit")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(context.Background(), opts); err != nil {
		slog.Error("identity backfill failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) error {
	if opts.dsn == "" {
		return fmt.Errorf("--onex-mysql-dsn is required")
	}
	if opts.appID == "" || opts.appSecret == "" {
		return fmt.Errorf("--lark-app-id and --lark-app-secret are required to resolve user_ids")
	}

	// This tool only ever rewrites the frozen MySQL deployment, so the dialect
	// is pinned rather than configured; naming it anyway keeps the table names
	// coming from the same place the services use.
	dbschema.Configure(dbschema.MySQL)

	db, err := gorm.Open(gormmysql.Open(opts.dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("connect to MySQL: %w", err)
	}

	selected, err := selectTargets(opts.tables)
	if err != nil {
		return err
	}

	cli := lark.NewClient(opts.appID, opts.appSecret, lark.WithLogger(larkkit.SDKLogger{}))

	b := &backfill{
		db:       db,
		resolver: larkkit.NewIdentityResolver(cli, opts.resolveSleep),
		apply:    opts.apply,
		limit:    opts.limit,
	}

	slog.Info("scanning for open_ids", "columns", len(selected), "apply", opts.apply)
	report, err := b.run(ctx, selected)
	if err != nil {
		return err
	}

	report.print(os.Stdout)

	if !opts.apply {
		slog.Info("dry run: nothing was written; re-run with --apply to rewrite the rows")
		return nil
	}
	slog.Info("backfill complete", "rows_rewritten", report.rowsRewritten, "rows_unresolved", report.rowsUnresolved)
	return nil
}

// selectTargets filters the column list by the --tables flag. Entries are
// written as table.column, which is the granularity that matters: a table can
// hold both a person column and a chat column, and only one of them is in scope.
func selectTargets(spec string) ([]target, error) {
	all := targets()

	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "all") {
		return all, nil
	}

	byName := make(map[string]target, len(all))
	for _, t := range all {
		byName[t.name()] = t
	}

	var out []target
	for _, part := range strings.Split(spec, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		t, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown column %q; known columns: %s", name, strings.Join(targetNames(), ", "))
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--tables selected no columns")
	}
	return out, nil
}
