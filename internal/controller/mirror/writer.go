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

package mirror

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/pkg/readmodel"
)

//go:embed schema.sql
var schemaSQL string

// maxRowsPerStatement bounds one multi-row statement. Seventeen columns per row
// leaves ample headroom under PostgreSQL's 65535 bind-parameter limit, and a
// smaller statement is also a shorter lock: the mirror is written while the
// control plane keeps serving.
const maxRowsPerStatement = 1000

// schemaPlaceholder is what schema.sql uses where the schema name belongs.
const schemaPlaceholder = "{{schema}}"

// RowWriter is what a reconciler needs: it only ever writes rows.
//
// Keeping this narrower than Writer is what lets the Batcher stand in for the
// database writer without having to proxy every read and DDL method it would
// never call.
type RowWriter interface {
	// Upsert writes whole rows; the newest resourceVersion wins per object.
	Upsert(ctx context.Context, rows []Row) error
	// Delete removes rows by identity; a row that is already gone is success.
	Delete(ctx context.Context, keys []Key) error
	// StatusOnly refreshes the status columns of rows that already exist. A key
	// with no row is skipped: status never creates a row.
	StatusOnly(ctx context.Context, rows []Row) error
}

// Writer is the only way this package touches the database.
//
// Everything above it — the reconciler, the sweeper, and a future streaming
// projector — is written against these interfaces, so replacing the reconciler
// with a projector later does not move any SQL.
type Writer interface {
	RowWriter
	// Load reads one kind's identity and hashes. It seeds the in-memory index at
	// start-up, which is what lets a sweep tell "this object is gone" from "this
	// process has simply not seen it yet".
	Load(ctx context.Context, group, kind string, namespaces []string) ([]CachedEntry, error)
	// Count returns the row count of one kind.
	Count(ctx context.Context, group, kind string) (int64, error)
	// PutWatermark records one kind's projection state.
	PutWatermark(ctx context.Context, w readmodel.Watermark) error
	// Watermark reads one kind's projection state.
	Watermark(ctx context.Context, group, kind string) (readmodel.Watermark, error)
	// EnsureSchema creates the schema, the tables, the indexes and one partition
	// per kind. It is idempotent.
	EnsureSchema(ctx context.Context, kinds []Kind) error
}

// ErrWatermarkMissing reports a kind that has never been swept.
var ErrWatermarkMissing = errors.New("mirror: no projection watermark for this kind")

// CachedEntry is one row's identity and hashes, without its payload. It is what
// the in-memory index holds and what a start-up load reads back.
type CachedEntry struct {
	Namespace  string
	Name       string
	SpecHash   string
	StatusHash string
}

// objectColumns is the column order every statement below binds against. It is
// one list, not three, so a column added to the table cannot silently shift the
// arguments of a statement that forgot about it.
var objectColumns = []string{
	"group_name", "kind", "namespace", "name",
	"uid", "resource_version", "generation", "created_at",
	"enabled", "display_name", "owner",
	"labels", "annotations", "spec", "status",
	"spec_hash", "status_hash", "observed_at",
}

// PostgresWriter is the Writer implementation.
//
// The statements are hand-written PostgreSQL rather than GORM's upsert helper
// because the parts that keep the projection correct — ON CONFLICT ... DO
// UPDATE ... WHERE, jsonb, list partitioning — are exactly the parts GORM cannot
// express. The connection itself is still GORM's, so pooling and logging carry
// over from the rest of the services.
type PostgresWriter struct {
	db     *gorm.DB
	schema string

	upsertSQL      string
	deleteSQL      string
	statusSQL      string
	loadSQL        string
	countSQL       string
	stateUpsertSQL string
	stateSelectSQL string
}

// NewPostgresWriter builds a writer against an already-open connection.
func NewPostgresWriter(db *gorm.DB, schema string) (*PostgresWriter, error) {
	if err := readmodel.ValidateSchema(schema); err != nil {
		return nil, err
	}

	object := readmodel.Object(schema)
	state := readmodel.State(schema)

	w := &PostgresWriter{
		db:     db,
		schema: schema,
		upsertSQL: fmt.Sprintf(`INSERT INTO %s (%s) VALUES %%s
ON CONFLICT (group_name, kind, namespace, name) DO UPDATE SET
  uid = excluded.uid,
  resource_version = excluded.resource_version,
  generation = excluded.generation,
  enabled = excluded.enabled,
  display_name = excluded.display_name,
  owner = excluded.owner,
  labels = excluded.labels,
  annotations = excluded.annotations,
  spec = excluded.spec,
  status = excluded.status,
  spec_hash = excluded.spec_hash,
  status_hash = excluded.status_hash,
  observed_at = excluded.observed_at
WHERE excluded.uid <> %s.uid
   OR excluded.resource_version >= %s.resource_version`,
			object, strings.Join(objectColumns, ", "), object, object),
		deleteSQL: fmt.Sprintf(
			`DELETE FROM %s WHERE (group_name, kind, namespace, name) IN (%%s)`, object),
		statusSQL: fmt.Sprintf(`UPDATE %s AS o
SET status = v.status, status_hash = v.status_hash, observed_at = v.observed_at
FROM (VALUES %%s) AS v(group_name, kind, namespace, name, status, status_hash, observed_at)
WHERE o.group_name = v.group_name AND o.kind = v.kind
  AND o.namespace = v.namespace AND o.name = v.name`, object),
		loadSQL: fmt.Sprintf(`SELECT namespace, name, spec_hash, status_hash
FROM %s WHERE group_name = $1 AND kind = $2`, object),
		countSQL: fmt.Sprintf(`SELECT count(*) FROM %s WHERE group_name = $1 AND kind = $2`, object),
		stateUpsertSQL: fmt.Sprintf(`INSERT INTO %s
  (group_name, kind, object_count, last_sweep_at, last_rv, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (group_name, kind) DO UPDATE SET
  object_count = excluded.object_count,
  last_sweep_at = excluded.last_sweep_at,
  last_rv = excluded.last_rv,
  updated_at = excluded.updated_at`, state),
		stateSelectSQL: fmt.Sprintf(`SELECT object_count, last_sweep_at, last_rv, updated_at
FROM %s WHERE group_name = $1 AND kind = $2`, state),
	}
	return w, nil
}

// Upsert writes rows in bounded statements.
func (w *PostgresWriter) Upsert(ctx context.Context, rows []Row) error {
	for start := 0; start < len(rows); start += maxRowsPerStatement {
		end := min(start+maxRowsPerStatement, len(rows))
		if err := w.upsertChunk(ctx, rows[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (w *PostgresWriter) upsertChunk(ctx context.Context, rows []Row) error {
	if len(rows) == 0 {
		return nil
	}

	var (
		values strings.Builder
		args   = make([]any, 0, len(rows)*len(objectColumns))
	)
	for i, row := range rows {
		if i > 0 {
			values.WriteString(", ")
		}
		base := len(args)
		// The jsonb columns are cast explicitly: a []byte binds as bytea, which
		// PostgreSQL will not implicitly coerce to jsonb.
		fmt.Fprintf(&values, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, "+
			"$%d::jsonb, $%d::jsonb, $%d::jsonb, $%d::jsonb, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9,
			base+10, base+11, base+12, base+13, base+14, base+15, base+16, base+17, base+18)
		args = append(args,
			row.Group, row.Kind, row.Namespace, row.Name,
			row.UID, row.ResourceVersion, row.Generation, row.CreatedAt,
			row.Enabled, row.DisplayName, row.Owner,
			row.Labels, row.Annotations, row.Spec, row.Status,
			row.SpecHash, row.StatusHash, row.ObservedAt,
		)
	}

	sql := fmt.Sprintf(w.upsertSQL, values.String())
	if err := w.db.WithContext(ctx).Exec(sql, args...).Error; err != nil {
		return fmt.Errorf("mirror: upsert %d row(s): %w", len(rows), err)
	}
	return nil
}

// Delete removes rows by identity.
func (w *PostgresWriter) Delete(ctx context.Context, keys []Key) error {
	for start := 0; start < len(keys); start += maxRowsPerStatement {
		end := min(start+maxRowsPerStatement, len(keys))
		if err := w.deleteChunk(ctx, keys[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (w *PostgresWriter) deleteChunk(ctx context.Context, keys []Key) error {
	if len(keys) == 0 {
		return nil
	}

	var (
		tuples strings.Builder
		args   = make([]any, 0, len(keys)*4)
	)
	for i, key := range keys {
		if i > 0 {
			tuples.WriteString(", ")
		}
		base := len(args)
		fmt.Fprintf(&tuples, "($%d, $%d, $%d, $%d)", base+1, base+2, base+3, base+4)
		args = append(args, key.Group, key.Kind, key.Namespace, key.Name)
	}

	sql := fmt.Sprintf(w.deleteSQL, tuples.String())
	if err := w.db.WithContext(ctx).Exec(sql, args...).Error; err != nil {
		return fmt.Errorf("mirror: delete %d row(s): %w", len(keys), err)
	}
	return nil
}

// StatusOnly refreshes status without touching spec.
func (w *PostgresWriter) StatusOnly(ctx context.Context, rows []Row) error {
	for start := 0; start < len(rows); start += maxRowsPerStatement {
		end := min(start+maxRowsPerStatement, len(rows))
		if err := w.statusChunk(ctx, rows[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (w *PostgresWriter) statusChunk(ctx context.Context, rows []Row) error {
	if len(rows) == 0 {
		return nil
	}

	var (
		tuples strings.Builder
		args   = make([]any, 0, len(rows)*7)
	)
	for i, row := range rows {
		if i > 0 {
			tuples.WriteString(", ")
		}
		base := len(args)
		// The casts are load-bearing: a parameter inside a VALUES list gets its
		// type inferred as text, and PostgreSQL will not coerce text into the
		// timestamptz target column (42804). The INSERT path inlines its
		// literals, which is why it never hit this.
		fmt.Fprintf(&tuples, "($%d, $%d, $%d, $%d, $%d::jsonb, $%d, $%d::timestamptz)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7)
		args = append(args,
			row.Group, row.Kind, row.Namespace, row.Name,
			row.Status, row.StatusHash, row.ObservedAt,
		)
	}

	sql := fmt.Sprintf(w.statusSQL, tuples.String())
	if err := w.db.WithContext(ctx).Exec(sql, args...).Error; err != nil {
		return fmt.Errorf("mirror: refresh status of %d row(s): %w", len(rows), err)
	}
	return nil
}

// Load reads one kind's identity and hashes.
//
// namespaces narrows the read to the cache scope: a manager watching one
// namespace must not load rows from the others, or its first sweep would delete
// them for being absent.
func (w *PostgresWriter) Load(
	ctx context.Context, group, kind string, namespaces []string,
) ([]CachedEntry, error) {
	sql := w.loadSQL
	args := []any{group, kind}
	if len(namespaces) > 0 {
		sql += " AND namespace = ANY($3)"
		args = append(args, namespaces)
	}

	entries := make([]CachedEntry, 0, 64)
	err := w.db.WithContext(ctx).Raw(sql, args...).Scan(&entries).Error
	if err != nil {
		return nil, fmt.Errorf("mirror: load %s: %w", kind, err)
	}
	return entries, nil
}

// Count returns how many rows one kind holds.
func (w *PostgresWriter) Count(ctx context.Context, group, kind string) (int64, error) {
	var count int64
	if err := w.db.WithContext(ctx).Raw(w.countSQL, group, kind).Scan(&count).Error; err != nil {
		return 0, fmt.Errorf("mirror: count %s: %w", kind, err)
	}
	return count, nil
}

// PutWatermark records one kind's projection state.
func (w *PostgresWriter) PutWatermark(ctx context.Context, state readmodel.Watermark) error {
	err := w.db.WithContext(ctx).Exec(w.stateUpsertSQL,
		state.Group, state.Kind, state.ObjectCount, state.LastSweepAt, state.LastRV, state.UpdatedAt,
	).Error
	if err != nil {
		return fmt.Errorf("mirror: write watermark for %s: %w", state.Kind, err)
	}
	return nil
}

// Watermark reads one kind's projection state.
func (w *PostgresWriter) Watermark(
	ctx context.Context, group, kind string,
) (readmodel.Watermark, error) {
	var row struct {
		ObjectCount int64
		LastSweepAt *time.Time
		LastRV      int64
		UpdatedAt   time.Time
	}
	err := w.db.WithContext(ctx).Raw(w.stateSelectSQL, group, kind).Scan(&row).Error
	if err != nil {
		return readmodel.Watermark{}, fmt.Errorf("mirror: read watermark for %s: %w", kind, err)
	}
	if row.UpdatedAt.IsZero() {
		return readmodel.Watermark{}, fmt.Errorf("%w: %s", ErrWatermarkMissing, kind)
	}

	state := readmodel.Watermark{
		Group:       group,
		Kind:        kind,
		ObjectCount: row.ObjectCount,
		LastRV:      row.LastRV,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.LastSweepAt != nil {
		state.LastSweepAt = *row.LastSweepAt
	}
	return state, nil
}

// EnsureSchema creates the schema, the tables, the indexes and one partition per
// kind.
//
// Partitioning by kind keeps a per-kind sweep and a per-kind vacuum proportional
// to that kind, which is the point once one kind dominates the table. The
// partitions are created up front from the registry rather than lazily: a
// partition added after its rows already landed in the DEFAULT partition cannot
// be created without moving them.
func (w *PostgresWriter) EnsureSchema(ctx context.Context, kinds []Kind) error {
	// The schema name is configuration, and PostgreSQL has no bind parameter for
	// an identifier — which is why ValidateSchema rejects anything that is not a
	// plain lower-case identifier before it can reach here.
	ddl := strings.ReplaceAll(schemaSQL, schemaPlaceholder, w.schema)
	for _, statement := range splitStatements(ddl) {
		if err := w.db.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("mirror: apply schema: %w", err)
		}
	}

	for _, kind := range kinds {
		statement := fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES IN ('%s')`,
			partitionName(w.schema, kind.Kind), readmodel.Object(w.schema), kind.Kind,
		)
		if err := w.db.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("mirror: create partition for %s: %w", kind.Kind, err)
		}
	}
	return nil
}

// partitionName is the identifier of one kind's partition.
func partitionName(schema, kind string) string {
	return readmodel.Object(schema) + "_" + strings.ToLower(kind)
}

// splitStatements turns the embedded DDL into executable statements.
//
// Line comments are stripped first so a semicolon inside one cannot split a
// statement; the file carries no semicolons inside literals, which is what keeps
// this simple split honest.
func splitStatements(sql string) []string {
	var (
		statements []string
		current    strings.Builder
	)
	for _, line := range strings.Split(sql, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "--") {
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			if statement := strings.TrimSpace(current.String()); statement != "" {
				statements = append(statements, strings.TrimSuffix(statement, ";"))
			}
			current.Reset()
		}
	}
	if statement := strings.TrimSpace(current.String()); statement != "" {
		statements = append(statements, statement)
	}
	return statements
}
