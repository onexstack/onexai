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
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/pkg/dbschema"
)

// TestVectorIndexAgainstPostgres proves the pgvector path end to end: the
// mirror column is added, the per-width HNSW index builds, nearest-neighbour
// retrieval orders by cosine distance, and a content change drops the stale
// vector. It is skipped unless MEMORY_STORE_TEST_PG_DSN is set, because it
// writes rows (all under a dedicated namespace) and needs the extension.
func TestVectorIndexAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MEMORY_STORE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set MEMORY_STORE_TEST_PG_DSN to run the pgvector integration test")
	}
	dbschema.Configure(dbschema.Postgres)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	s := New(db)
	if err := s.EnableVector(ctx); err != nil {
		t.Fatalf("EnableVector: %v", err)
	}
	if !s.VectorEnabled() {
		t.Skip("the database has no pgvector extension")
	}
	if err := s.EnsureVectorIndex(ctx, 4); err != nil {
		t.Fatalf("EnsureVectorIndex: %v", err)
	}

	const (
		namespace = "memory-vector-live"
		memory    = "vector-live"
		subject   = "u1"
	)
	t.Cleanup(func() {
		db.Unscoped().Where("namespace = ? AND memory = ?", namespace, memory).
			Delete(&model.MemoryEntryM{})
	})

	now := time.Now().UnixMilli()
	rows := []*model.MemoryEntryM{
		entry(namespace, memory, subject, "a", "database preference", now),
		entry(namespace, memory, subject, "b", "deployment preference", now),
		entry(namespace, memory, subject, "c", "unrelated note", now),
	}
	for _, row := range rows {
		if err := s.Create(ctx, row); err != nil {
			t.Fatalf("create %s: %v", row.ID, err)
		}
	}
	vectors := map[string][]float32{
		"a": {1, 0, 0, 0},
		"b": {0.9, 0.1, 0, 0},
		"c": {0, 1, 0, 0},
	}
	for id, vec := range vectors {
		if err := s.UpdateEmbedding(ctx, id, memory, "test-model", "v1", "ready", vec, ""); err != nil {
			t.Fatalf("UpdateEmbedding %s: %v", id, err)
		}
	}
	if err := s.EnsureVectorIndexes(ctx); err != nil {
		t.Fatalf("EnsureVectorIndexes: %v", err)
	}

	got, err := s.nearestIDs(ctx, namespace, memory, "", subject, []float32{1, 0, 0, 0}, 3)
	if err != nil {
		t.Fatalf("nearestIDs: %v", err)
	}
	if len(got) < 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("nearestIDs order = %v, want [a b c]", got)
	}

	keyword, err := s.keywordIDs(ctx, namespace, memory, "", subject,
		[]string{"deployment", "unrelated"}, 10)
	if err != nil {
		t.Fatalf("keywordIDs: %v", err)
	}
	if len(keyword) != 2 {
		t.Fatalf("keywordIDs = %v, want two rows", keyword)
	}

	// Changing the content must drop both the blob and the pgvector copy so a
	// stale fact cannot be ranked while the re-embed is pending.
	stale := &model.MemoryEntryM{ID: "a", MemoryUID: memory, Subject: subject,
		Content: "changed", Type: "semantic", Status: "active", Importance: 0.5,
		Confidence: 0.5, EmbeddingStatus: "pending", Version: 2, UpdatedAt: now}
	if err := s.Update(ctx, stale); err != nil {
		t.Fatalf("Update: %v", err)
	}
	var count int64
	if err := db.Model(&model.MemoryEntryM{}).
		Where("id = ? AND embedding IS NULL AND embedding_vec IS NULL", "a").
		Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("stale vector was not cleared (count=%d, err=%v)", count, err)
	}
}

func entry(namespace, memory, subject, id, content string, now int64) *model.MemoryEntryM {
	return &model.MemoryEntryM{
		ID:               id,
		Namespace:        namespace,
		Memory:           memory,
		MemoryUID:        memory,
		Subject:          subject,
		Type:             "semantic",
		Status:           "active",
		Content:          content,
		DedupeKey:        id,
		EmbeddingStatus:  "pending",
		EmbeddingModel:   "test-model",
		EmbeddingVersion: "v1",
		Source:           "manual",
		Importance:       0.5,
		Confidence:       0.5,
		Version:          1,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}
