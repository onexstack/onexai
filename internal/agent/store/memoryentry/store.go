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

// Package memoryentry persists long-term memory entries. Vector ranking is done
// in the service so the same code works on SQLite, MySQL and PostgreSQL.
package memoryentry

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/pkg/vector"
)

// ListFilter narrows an entry listing.
type ListFilter struct {
	Namespace string
	Memory    string
	Agent     string
	Subject   string
	Type      string
	Status    string
	Source    string
	Query     string
	SessionID string
	RunID     string
	Tag       string
	Offset    int
	Limit     int
	Sort      string
}

// Stats is an aggregate over one memory, optionally narrowed to a subject.
type Stats struct {
	Total             int64            `json:"total"`
	ByType            map[string]int64 `json:"byType"`
	ByStatus          map[string]int64 `json:"byStatus"`
	PendingEmbeddings int64            `json:"pendingEmbeddings"`
	FailedEmbeddings  int64            `json:"failedEmbeddings"`
	Subjects          int64            `json:"subjects"`
}

// Subject is one aggregated memory owner.
type Subject struct {
	Subject       string `json:"subject"`
	Entries       int64  `json:"entries"`
	LastCreatedAt int64  `json:"lastCreatedAt"`
}

// Store is the durable memory entry surface.
type Store struct {
	db *gorm.DB

	// The pgvector accelerator is optional. Detection runs once per process
	// unless it fails; vectorMu also serialises index creation.
	vectorMu        sync.Mutex
	vectorChecked   atomic.Bool
	vectorOK        atomic.Bool
	vectorIterative atomic.Bool
	vectorIndexed   map[int]bool
}

// New returns a store over db.
func New(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) base(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).Model(&model.MemoryEntryM{}).
		Where("deleted_at = 0 AND status <> ?", "deleted")
}

// Create inserts one entry.
func (s *Store) Create(ctx context.Context, entry *model.MemoryEntryM) error {
	return s.db.WithContext(ctx).Create(entry).Error
}

// Get returns one entry owned by subject.
func (s *Store) Get(
	ctx context.Context, namespace, memory, subject, id string,
) (*model.MemoryEntryM, error) {
	var row model.MemoryEntryM
	err := s.base(ctx).
		Where("namespace = ? AND memory = ? AND subject = ? AND id = ?", namespace, memory, subject, id).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// GetByID returns one entry by id within a memory, without a subject filter.
// The caller is responsible for having applied the subject policy.
func (s *Store) GetByID(ctx context.Context, namespace, memory, id string) (*model.MemoryEntryM, error) {
	var row model.MemoryEntryM
	err := s.base(ctx).
		Where("namespace = ? AND memory = ? AND id = ?", namespace, memory, id).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindActive returns the active entry with the same dedupe key, if any.
func (s *Store) FindActive(
	ctx context.Context, memoryUID, subject, dedupeKey string,
) (*model.MemoryEntryM, error) {
	var row model.MemoryEntryM
	err := s.base(ctx).
		Where("memory_uid = ? AND subject = ? AND dedupe_key = ? AND status = ?",
			memoryUID, subject, dedupeKey, "active").
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// List returns one page and the total behind it.
func (s *Store) List(ctx context.Context, f ListFilter) (int64, []*model.MemoryEntryM, error) {
	query := s.base(ctx).
		Where("namespace = ? AND memory = ?", f.Namespace, f.Memory)
	if f.Agent != "" {
		query = query.Where("agent = ?", f.Agent)
	}
	if f.Subject != "" {
		query = query.Where("subject = ?", f.Subject)
	}
	if f.Type != "" {
		query = query.Where("type = ?", f.Type)
	}
	if f.Status != "" {
		query = query.Where("status = ?", f.Status)
	} else {
		// Hide entries whose TTL has passed even before the hourly worker marks
		// them expired. Retrieval already filters them; the admin list should
		// agree with retrieval.
		query = query.Where("status = ?", "active").
			Where("expires_at = 0 OR expires_at > ?", time.Now().UnixMilli())
	}
	if f.Source != "" {
		query = query.Where("source = ?", f.Source)
	}
	if f.SessionID != "" {
		query = query.Where("session_id = ?", f.SessionID)
	}
	if f.RunID != "" {
		query = query.Where("run_id = ?", f.RunID)
	}
	if f.Tag != "" {
		query = query.Where("tags LIKE ?", "%"+f.Tag+"%")
	}
	if f.Query != "" {
		like := "%" + strings.TrimSpace(f.Query) + "%"
		query = query.Where("content LIKE ?", like)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return 0, nil, err
	}

	order := "updated_at DESC"
	switch f.Sort {
	case "createdAt", "-createdAt":
		order = "created_at DESC"
	case "importance", "-importance":
		order = "importance DESC"
	case "lastAccessedAt", "-lastAccessedAt":
		order = "last_accessed_at DESC"
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	var rows []*model.MemoryEntryM
	return total, rows, query.Order(order).Offset(f.Offset).Limit(limit).Find(&rows).Error
}

// Candidates returns the rows one search may rank: active, unexpired, newest
// first, capped by limit.
func (s *Store) Candidates(
	ctx context.Context, namespace, memory, agent, subject string, limit int,
) ([]*model.MemoryEntryM, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	query := s.base(ctx).
		Where("namespace = ? AND memory = ? AND subject = ? AND status = ?",
			namespace, memory, subject, "active").
		Where("expires_at = 0 OR expires_at > ?", time.Now().UnixMilli())
	if agent != "" {
		query = query.Where("agent = ?", agent)
	}
	var rows []*model.MemoryEntryM
	return rows, query.Order("created_at DESC").Limit(limit).Find(&rows).Error
}

// Update writes the mutable fields of one entry.
func (s *Store) Update(ctx context.Context, entry *model.MemoryEntryM) error {
	if err := s.db.WithContext(ctx).Model(&model.MemoryEntryM{}).
		Where("id = ? AND memory_uid = ? AND subject = ?", entry.ID, entry.MemoryUID, entry.Subject).
		Select("content", "type", "status", "importance", "confidence", "tags", "metadata",
			"expires_at", "embedding_status", "embedding_model", "embedding_version",
			"embedding_attempts", "embedding_error", "next_attempt_at", "version", "updated_at").
		Updates(entry).Error; err != nil {
		return err
	}
	// A content change marks the embedding pending. The old vector no longer
	// describes the text, so drop both copies instead of ranking a stale fact
	// until the asynchronous re-embed finishes.
	if entry.EmbeddingStatus == "pending" {
		entry.Embedding = nil
		if err := s.EnableVector(ctx); err != nil {
			slog.WarnContext(ctx, "memory: pgvector check failed while clearing a stale vector", "error", err)
		}
		updates := map[string]any{"embedding": nil}
		if s.VectorEnabled() {
			updates[vectorColumn] = nil
		}
		return s.db.WithContext(ctx).Model(&model.MemoryEntryM{}).
			Where("id = ? AND memory_uid = ?", entry.ID, entry.MemoryUID).
			Updates(updates).Error
	}
	return nil
}

// UpdateEmbedding writes the embedding result of one entry.
func (s *Store) UpdateEmbedding(
	ctx context.Context, id, memoryUID, modelName, version, status string,
	embedding []float32, errText string,
) error {
	// A write can beat the maintenance worker to the first search, so the
	// optional column has to exist before the first embedding lands. This is a
	// no-op after the first call.
	if len(embedding) > 0 {
		if err := s.EnableVector(ctx); err != nil {
			slog.WarnContext(ctx, "memory: pgvector check failed; keeping the portable blob", "error", err)
		}
	}
	updates := map[string]any{
		"embedding":          vector.Encode(embedding),
		"embedding_model":    modelName,
		"embedding_version":  version,
		"embedding_status":   status,
		"embedding_error":    errText,
		"embedding_attempts": gorm.Expr("embedding_attempts + 1"),
	}
	if s.VectorEnabled() {
		if len(embedding) == 0 {
			updates[vectorColumn] = nil
		} else {
			updates[vectorColumn] = gorm.Expr("?::vector", vector.Literal(embedding))
		}
	}
	return s.db.WithContext(ctx).Model(&model.MemoryEntryM{}).
		Where("id = ? AND memory_uid = ?", id, memoryUID).
		Updates(updates).Error
}

// Archive marks entries archived without deleting their content.
func (s *Store) Archive(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	return s.db.WithContext(ctx).Model(&model.MemoryEntryM{}).
		Where("id IN ?", ids).
		Updates(map[string]any{"status": "archived", "updated_at": now}).Error
}

// QuotaVictims returns the ids above the per-subject cap, least important first.
func (s *Store) QuotaVictims(
	ctx context.Context, namespace, memory, subject string, keep int,
) ([]string, error) {
	if keep < 0 {
		keep = 0
	}
	var ids []string
	err := s.base(ctx).
		Where("namespace = ? AND memory = ? AND subject = ? AND status = ?",
			namespace, memory, subject, "active").
		Order("importance DESC, updated_at DESC").
		Offset(keep).
		Limit(1000).
		Pluck("id", &ids).Error
	return ids, err
}

// ListNeedingReembed returns entries whose embedding is not the current model
// version, plus the total that still needs work.
func (s *Store) ListNeedingReembed(
	ctx context.Context, namespace, memory, version string, limit int,
) ([]*model.MemoryEntryM, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := s.base(ctx).
		Where("namespace = ? AND memory = ?", namespace, memory).
		Where("(embedding_version <> ? OR embedding_status <> ?)", version, "ready")

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*model.MemoryEntryM
	err := query.Order("updated_at ASC").Limit(limit).Find(&rows).Error
	return rows, total, err
}

// PruneExpired marks active entries whose expiry has passed as expired.
func (s *Store) PruneExpired(ctx context.Context, now int64, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	var ids []string
	if err := s.base(ctx).
		Where("status = ? AND expires_at > 0 AND expires_at <= ?", "active", now).
		Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := s.db.WithContext(ctx).Model(&model.MemoryEntryM{}).
		Where("id IN ?", ids).
		Updates(map[string]any{"status": "expired", "updated_at": now})
	return result.RowsAffected, result.Error
}

// PurgeDeleted hard-deletes soft-deleted rows older than before.
func (s *Store) PurgeDeleted(ctx context.Context, before int64, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	var ids []string
	if err := s.db.WithContext(ctx).Model(&model.MemoryEntryM{}).
		Where("status = ? AND deleted_at > 0 AND deleted_at <= ?", "deleted", before).
		Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.MemoryEntryM{})
	return result.RowsAffected, result.Error
}

// Touch increments the access count of the entries a search returned.
func (s *Store) Touch(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	return s.db.WithContext(ctx).Model(&model.MemoryEntryM{}).
		Where("id IN ?", ids).
		Updates(map[string]any{
			"access_count":     gorm.Expr("access_count + 1"),
			"last_accessed_at": now,
		}).Error
}

// SoftDelete marks one entry deleted.
func (s *Store) SoftDelete(ctx context.Context, id, memoryUID, subject string) error {
	now := time.Now().UnixMilli()
	return s.db.WithContext(ctx).Model(&model.MemoryEntryM{}).
		Where("id = ? AND memory_uid = ? AND subject = ?", id, memoryUID, subject).
		Updates(map[string]any{"status": "deleted", "deleted_at": now, "updated_at": now}).Error
}

// BatchSoftDelete marks many entries deleted, returning per-id errors.
func (s *Store) BatchSoftDelete(
	ctx context.Context, memoryUID, subject string, ids []string,
) map[string]error {
	out := make(map[string]error, len(ids))
	for _, id := range ids {
		out[id] = s.SoftDelete(ctx, id, memoryUID, subject)
	}
	return out
}

// Purge soft-deletes every entry of one subject.
func (s *Store) Purge(ctx context.Context, namespace, memory, subject string) (int64, error) {
	now := time.Now().UnixMilli()
	result := s.db.WithContext(ctx).Model(&model.MemoryEntryM{}).
		Where("namespace = ? AND memory = ? AND subject = ? AND deleted_at = 0", namespace, memory, subject).
		Updates(map[string]any{"status": "deleted", "deleted_at": now, "updated_at": now})
	return result.RowsAffected, result.Error
}

// Stats aggregates one memory, optionally narrowed to agent/subject.
func (s *Store) Stats(ctx context.Context, namespace, memory, agent, subject string) (*Stats, error) {
	base := func() *gorm.DB {
		query := s.base(ctx).Where("namespace = ? AND memory = ?", namespace, memory)
		if agent != "" {
			query = query.Where("agent = ?", agent)
		}
		if subject != "" {
			query = query.Where("subject = ?", subject)
		}
		return query
	}

	out := &Stats{ByType: map[string]int64{}, ByStatus: map[string]int64{}}
	if err := base().Count(&out.Total).Error; err != nil {
		return nil, err
	}

	var typeCounts []struct {
		Type  string
		Count int64
	}
	if err := base().Select("type, count(*) as count").Group("type").Scan(&typeCounts).Error; err != nil {
		return nil, err
	}
	for _, item := range typeCounts {
		out.ByType[item.Type] = item.Count
	}

	var statusCounts []struct {
		Status string
		Count  int64
	}
	if err := base().Select("status, count(*) as count").Group("status").Scan(&statusCounts).Error; err != nil {
		return nil, err
	}
	for _, item := range statusCounts {
		out.ByStatus[item.Status] = item.Count
	}

	if err := base().Where("embedding_status = ?", "pending").Count(&out.PendingEmbeddings).Error; err != nil {
		return nil, err
	}
	if err := base().Where("embedding_status = ?", "failed").Count(&out.FailedEmbeddings).Error; err != nil {
		return nil, err
	}
	if err := base().Distinct("subject").Count(&out.Subjects).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// Subjects aggregates subjects for one memory.
func (s *Store) Subjects(
	ctx context.Context, namespace, memory, agent, q string, offset, limit int,
) (int64, []*Subject, error) {
	base := func() *gorm.DB {
		query := s.base(ctx).Where("namespace = ? AND memory = ?", namespace, memory)
		if agent != "" {
			query = query.Where("agent = ?", agent)
		}
		if q != "" {
			query = query.Where("subject LIKE ?", "%"+q+"%")
		}
		return query
	}

	var total int64
	if err := base().Distinct("subject").Count(&total).Error; err != nil {
		return 0, nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var rows []*Subject
	err := base().Select("subject, count(*) as entries, max(created_at) as last_created_at").
		Group("subject").
		Order("last_created_at DESC").
		Offset(offset).
		Limit(limit).
		Scan(&rows).Error
	return total, rows, err
}
