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

package memoryentry

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/vector"
)

// vectorColumn is the pgvector mirror of the portable embedding blob. It is
// only populated (and only exists) on PostgreSQL with the vector extension.
const vectorColumn = "embedding_vec"

// maxHNSWDimensions is pgvector's ceiling for an indexed vector. Anything
// wider is still searchable through an exact distance scan.
const maxHNSWDimensions = 2000

// CandidateFilter selects the rows one retrieval may rank.
//
// When a query vector is supplied and pgvector is available, the candidate set
// is the union of an HNSW nearest-neighbour scan and a lexical prefilter. That
// keeps a search independent of how many entries a subject has accumulated,
// while still giving the keyword branch a chance to contribute. Without
// pgvector it falls back to a bounded newest-first scan, which is what the
// in-process cosine path has always used.
type CandidateFilter struct {
	Namespace string
	Memory    string
	Agent     string
	Subject   string

	Vector []float32
	Terms  []string

	VectorLimit  int
	KeywordLimit int
	ScanLimit    int
}

// EnableVector checks for the pgvector extension and adds the mirror column on
// first use. A missing extension is not an error: retrieval stays in process,
// which is exactly what the portable blob was for. DDL failures are returned so
// the caller can log them, and the check is retried on the next call.
func (s *Store) EnableVector(ctx context.Context) error {
	if s.vectorChecked.Load() {
		return nil
	}
	s.vectorMu.Lock()
	defer s.vectorMu.Unlock()
	if s.vectorChecked.Load() {
		return nil
	}
	if s.db.Dialector.Name() != "postgres" {
		// MySQL and SQLite keep the blob and never expose this path.
		s.vectorChecked.Store(true)
		return nil
	}

	var version string
	if err := s.db.WithContext(ctx).
		Raw(`SELECT extversion FROM pg_extension WHERE extname = 'vector'`).
		Scan(&version).Error; err != nil {
		return err
	}
	if version == "" {
		s.vectorChecked.Store(true)
		slog.WarnContext(ctx,
			"memory: the pgvector extension is not installed; vector search stays in process",
			"hint", "CREATE EXTENSION vector")
		return nil
	}
	// Iterative scans (pgvector 0.8+) keep a filtered HNSW query from coming
	// back short: without it the graph walk stops after its candidate budget
	// even when the namespace/memory/subject filter rejected most of what it
	// saw.
	s.vectorIterative.Store(versionAtLeast(version, 0, 8, 0))

	if err := s.db.WithContext(ctx).Exec(fmt.Sprintf(
		"ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s vector", s.table(), vectorColumn,
	)).Error; err != nil {
		return err
	}

	s.vectorOK.Store(true)
	s.vectorChecked.Store(true)
	return nil
}

// VectorEnabled reports whether at least one search can use pgvector.
func (s *Store) VectorEnabled() bool { return s.vectorOK.Load() }

// EnsureVectorIndexes creates the partial HNSW index for every dimension that
// already has vectors. It runs once at startup so a deployment upgraded from
// the in-process path does not have to wait for the first write.
func (s *Store) EnsureVectorIndexes(ctx context.Context) error {
	if err := s.EnableVector(ctx); err != nil {
		return err
	}
	if !s.VectorEnabled() {
		return nil
	}
	// Entries embedded before pgvector was installed have the portable blob but
	// no mirror. Fill the mirror first so they come back into the indexed path.
	s.backfillVectors(ctx)

	var dims []int
	if err := s.db.WithContext(ctx).Raw(fmt.Sprintf(
		"SELECT DISTINCT vector_dims(%s) FROM %s WHERE %s IS NOT NULL",
		vectorColumn, s.table(), vectorColumn,
	)).Scan(&dims).Error; err != nil {
		return err
	}
	for _, dim := range dims {
		if err := s.EnsureVectorIndex(ctx, dim); err != nil {
			slog.WarnContext(ctx, "memory: the vector index could not be created",
				"dimensions", dim, "error", err)
		}
	}
	return nil
}

// backfillBatch bounds one pass over entries that predate the mirror column.
const backfillBatch = 500

// backfillVectors copies portable blobs into embedding_vec. It is idempotent
// and bounded; an entry that fails is retried at the next start, and the exact
// scan keeps it searchable meanwhile.
func (s *Store) backfillVectors(ctx context.Context) {
	for processed := 0; processed < 10000; processed += backfillBatch {
		var rows []*model.MemoryEntryM
		if err := s.db.WithContext(ctx).
			Select("id", "embedding").
			Where("embedding IS NOT NULL AND embedding_vec IS NULL").
			Limit(backfillBatch).
			Find(&rows).Error; err != nil {
			slog.WarnContext(ctx, "memory: vector backfill could not read entries", "error", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		for _, row := range rows {
			values := vector.Decode(row.Embedding)
			if len(values) == 0 {
				continue
			}
			if err := s.db.WithContext(ctx).
				Exec(fmt.Sprintf("UPDATE %s SET %s = ?::vector WHERE id = ?", s.table(), vectorColumn),
					vector.Literal(values), row.ID).Error; err != nil {
				slog.WarnContext(ctx, "memory: a vector could not be backfilled",
					"id", row.ID, "error", err)
			}
		}
		if len(rows) < backfillBatch {
			return
		}
	}
}

// EnsureVectorIndex creates the HNSW index for one embedding width.
//
// The column has no declared width because one table serves memories backed by
// different embedding models. An expression index over a cast, limited by
// vector_dims, gives every width its own index over the same column:
//
//	CREATE INDEX ... USING hnsw ((embedding_vec::vector(N)) vector_cosine_ops)
//	  WHERE vector_dims(embedding_vec) = N
//
// Queries must use the same cast expression to match the index; nearestIDs
// does. CONCURRENTLY keeps the build from blocking entry writes.
func (s *Store) EnsureVectorIndex(ctx context.Context, dims int) error {
	if dims <= 0 || dims > 65535 {
		return fmt.Errorf("memory: unsupported embedding width %d", dims)
	}
	if !s.VectorEnabled() {
		if err := s.EnableVector(ctx); err != nil {
			return err
		}
		if !s.VectorEnabled() {
			return nil
		}
	}

	s.vectorMu.Lock()
	defer s.vectorMu.Unlock()
	if s.vectorIndexed == nil {
		s.vectorIndexed = map[int]bool{}
	}
	if s.vectorIndexed[dims] {
		return nil
	}
	if dims > maxHNSWDimensions {
		// pgvector refuses to index vectors this wide. The exact <=> scan still
		// answers correctly, just without the graph.
		s.vectorIndexed[dims] = true
		slog.InfoContext(ctx, "memory: embedding width exceeds the HNSW limit; using exact scan",
			"dimensions", dims, "limit", maxHNSWDimensions)
		return nil
	}
	stmt := fmt.Sprintf(
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON %s USING hnsw ((%s::vector(%d)) vector_cosine_ops) "+
			"WHERE vector_dims(%s) = %d",
		fmt.Sprintf("idx_agent_memory_entry_vec_%d", dims),
		s.table(), vectorColumn, dims, vectorColumn, dims,
	)
	if err := s.db.WithContext(ctx).Exec(stmt).Error; err != nil {
		return err
	}
	s.vectorIndexed[dims] = true
	return nil
}

// SearchCandidates returns the rows a retrieval may rank.
func (s *Store) SearchCandidates(
	ctx context.Context, f CandidateFilter,
) ([]*model.MemoryEntryM, error) {
	if len(f.Vector) > 0 {
		if err := s.EnableVector(ctx); err != nil {
			slog.WarnContext(ctx, "memory: pgvector check failed; falling back to a scan", "error", err)
		}
		if s.VectorEnabled() {
			rows, err := s.indexedCandidates(ctx, f)
			if err == nil {
				return rows, nil
			}
			slog.WarnContext(ctx, "memory: indexed retrieval failed; falling back to a scan", "error", err)
		}
	}
	return s.Candidates(ctx, f.Namespace, f.Memory, f.Agent, f.Subject, f.ScanLimit)
}

func (s *Store) indexedCandidates(
	ctx context.Context, f CandidateFilter,
) ([]*model.MemoryEntryM, error) {
	ids, err := s.nearestIDs(ctx, f.Namespace, f.Memory, f.Agent, f.Subject, f.Vector, f.VectorLimit)
	if err != nil {
		return nil, err
	}
	if len(f.Terms) > 0 {
		keywordIDs, keywordErr := s.keywordIDs(
			ctx, f.Namespace, f.Memory, f.Agent, f.Subject, f.Terms, f.KeywordLimit)
		if keywordErr != nil {
			// Lexical pruning is an addition, not the reason to lose the vector
			// hits; rank what the nearest-neighbour scan found.
			slog.WarnContext(ctx, "memory: keyword candidates unavailable", "error", keywordErr)
		} else {
			ids = mergeIDs(ids, keywordIDs)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return s.rowsByIDs(ctx, f.Namespace, f.Memory, f.Subject, ids)
}

func (s *Store) nearestIDs(
	ctx context.Context, namespace, memory, agent, subject string, query []float32, limit int,
) ([]string, error) {
	if len(query) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	dims := len(query)

	stmt := fmt.Sprintf(
		"SELECT id FROM %s WHERE namespace = ? AND memory = ? AND subject = ? "+
			"AND status = 'active' AND deleted_at = 0 AND (expires_at = 0 OR expires_at > ?) "+
			"AND vector_dims(%s) = ?",
		s.table(), vectorColumn,
	)
	args := []any{namespace, memory, subject, time.Now().UnixMilli(), dims}
	if agent != "" {
		stmt += " AND agent = ?"
		args = append(args, agent)
	}
	// The cast must look exactly like the index expression, width included.
	stmt += fmt.Sprintf(" ORDER BY (%s::vector(%d)) <=> ?::vector(%d) LIMIT ?",
		vectorColumn, dims, dims)
	args = append(args, vector.Literal(query), limit)

	var ids []string
	db := s.db.WithContext(ctx)
	if s.vectorIterative.Load() {
		// SET LOCAL needs the query and the setting in one transaction. The
		// setting is ignored by the planner when the index is not chosen, so it
		// is safe to apply unconditionally on 0.8+.
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SET LOCAL hnsw.iterative_scan = relaxed_order").Error; err != nil {
				return err
			}
			return tx.Raw(stmt, args...).Scan(&ids).Error
		})
		if err != nil {
			return nil, err
		}
		return ids, nil
	}
	if err := db.Raw(stmt, args...).Scan(&ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// versionAtLeast reports whether a dotted pgvector version is at least
// major.minor.patch. Unparsable versions are treated as older, which only
// costs the iterative-scan hint.
func versionAtLeast(version string, major, minor, patch int) bool {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 2 {
		return false
	}
	want := []int{major, minor, patch}
	for i := 0; i < len(want); i++ {
		if i >= len(parts) {
			return true
		}
		value, err := strconv.Atoi(parts[i])
		if err != nil {
			return false
		}
		if value != want[i] {
			return value > want[i]
		}
	}
	return true
}

func (s *Store) keywordIDs(
	ctx context.Context, namespace, memory, agent, subject string, terms []string, limit int,
) ([]string, error) {
	terms = likeTerms(terms)
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	conds := make([]string, 0, len(terms))
	args := []any{namespace, memory, subject, time.Now().UnixMilli()}
	for _, term := range terms {
		conds = append(conds, `content ILIKE ? ESCAPE '\'`)
		args = append(args, "%"+term+"%")
	}
	stmt := fmt.Sprintf(
		"SELECT id FROM %s WHERE namespace = ? AND memory = ? AND subject = ? "+
			"AND status = 'active' AND deleted_at = 0 AND (expires_at = 0 OR expires_at > ?)",
		s.table(),
	)
	if agent != "" {
		stmt += " AND agent = ?"
		args = append(args, agent)
	}
	stmt += " AND (" + strings.Join(conds, " OR ") + ") ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)

	var ids []string
	if err := s.db.WithContext(ctx).Raw(stmt, args...).Scan(&ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) rowsByIDs(
	ctx context.Context, namespace, memory, subject string, ids []string,
) ([]*model.MemoryEntryM, error) {
	var rows []*model.MemoryEntryM
	if len(ids) == 0 {
		return rows, nil
	}
	err := s.db.WithContext(ctx).
		Where("namespace = ? AND memory = ? AND subject = ? AND id IN ?", namespace, memory, subject, ids).
		Find(&rows).Error
	return rows, err
}

func (s *Store) table() string { return model.MemoryEntryM{}.TableName() }

// mergeIDs appends the ids of b that a does not already carry.
func mergeIDs(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, id := range list {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// likeTerms escapes LIKE metacharacters and drops terms too short to be
// selective. The escaping matters: a query containing "_" would otherwise
// match every entry.
func likeTerms(terms []string) []string {
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if len([]rune(term)) < 2 {
			continue
		}
		term = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term)
		out = append(out, term)
	}
	return out
}
