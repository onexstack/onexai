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
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/onexstack/onexai/pkg/declclient/mirrorstore"
	"github.com/onexstack/onexai/pkg/readmodel"
)

// The projection is PostgreSQL-specific — partitioning, jsonb, the ON CONFLICT
// guard — so it can only be verified against a real server.
//
//	ONEXAI_MIRROR_PG_DSN='postgres://user:pass@host:5432/db?sslmode=disable' \
//	  CGO_ENABLED=0 go test -run TestWriterLive ./internal/controller/mirror/
func TestWriterLive(t *testing.T) {
	dsn := os.Getenv("ONEXAI_MIRROR_PG_DSN")
	if dsn == "" {
		t.Skip("set ONEXAI_MIRROR_PG_DSN to run the projection against a real PostgreSQL")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()

	const schema = "mirror_livetest"
	writer, err := NewPostgresWriter(db, schema)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
		t.Fatalf("drop stale schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error })

	kinds, err := Select([]string{"Agent"})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if err := writer.EnsureSchema(ctx, kinds); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	row, err := Project(agentKind(), testAgent(), now)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if err := writer.Upsert(ctx, []Row{row}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	loaded, err := writer.Load(ctx, row.Group, row.Kind, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Name != row.Name || loaded[0].SpecHash != row.SpecHash {
		t.Fatalf("load = %+v", loaded)
	}

	// An older resourceVersion must not overwrite a newer row.
	older := row
	older.ResourceVersion = row.ResourceVersion - 1
	older.Spec = []byte(`{"displayName":"older"}`)
	older.SpecHash = "older"
	if err := writer.Upsert(ctx, []Row{older}); err != nil {
		t.Fatalf("upsert stale: %v", err)
	}
	var display string
	if err := db.Raw("SELECT spec->>'displayName' FROM "+schema+".object WHERE name = ?", row.Name).
		Scan(&display).Error; err != nil {
		t.Fatalf("read display name: %v", err)
	}
	if display != "Helper" {
		t.Fatalf("a stale row overwrote a newer one: displayName = %q", display)
	}

	// A recreated object (same name, new UID) must win even with an equal
	// resourceVersion.
	recreated := row
	recreated.UID = "uid-2"
	recreated.Spec = []byte(`{"displayName":"Recreated"}`)
	recreated.SpecHash = "recreated"
	if err := writer.Upsert(ctx, []Row{recreated}); err != nil {
		t.Fatalf("upsert recreated: %v", err)
	}
	if err := db.Raw("SELECT spec->>'displayName' FROM "+schema+".object WHERE name = ?", row.Name).
		Scan(&display).Error; err != nil {
		t.Fatalf("read display name: %v", err)
	}
	if display != "Recreated" {
		t.Fatalf("a new object under the same name did not replace the row: %q", display)
	}

	// The status-only path goes through a VALUES list, where PostgreSQL infers a
	// bare parameter as text and refuses to store it in the timestamptz column.
	// The INSERT inlines its literals, so only a live server shows the difference.
	statusAt := now.Add(time.Minute).Truncate(time.Millisecond)
	statusOnly := recreated
	statusOnly.Status = []byte(`{"resolved":{"ready":true}}`)
	statusOnly.StatusHash = "status-2"
	statusOnly.ObservedAt = statusAt
	if err := writer.StatusOnly(ctx, []Row{statusOnly}); err != nil {
		t.Fatalf("status only: %v", err)
	}
	var (
		storedHash string
		observedAt time.Time
	)
	if err := db.Raw("SELECT status_hash, observed_at FROM "+schema+".object WHERE name = ?", row.Name).
		Row().Scan(&storedHash, &observedAt); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if storedHash != statusOnly.StatusHash || !observedAt.Equal(statusAt) {
		t.Fatalf("status-only update lost: hash = %q, observedAt = %v (want %v)",
			storedHash, observedAt, statusAt)
	}

	// The read side must see the row and answer a List in one page.
	reader, err := mirrorstore.New(mirrorstore.Config{
		DB:        db,
		Schema:    schema,
		Group:     row.Group,
		Kind:      row.Kind,
		MaxLag:    time.Minute,
		CountMode: mirrorstore.CountExact,
	}, func(_ context.Context, raw mirrorstore.RawRow) (string, error) { return raw.Name, nil })
	if err != nil {
		t.Fatalf("mirrorstore: %v", err)
	}
	if err := writer.PutWatermark(ctx, watermarkFor(row, now)); err != nil {
		t.Fatalf("watermark: %v", err)
	}
	total, names, err := reader.ListPage(ctx, map[string]string{"name": row.Name}, 0, 10)
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	if total != 1 || len(names) != 1 || names[0] != row.Name {
		t.Fatalf("list page = %d %v", total, names)
	}

	// A watermark older than the budget must refuse to answer.
	if err := writer.PutWatermark(ctx, watermarkFor(row, now.Add(-time.Hour))); err != nil {
		t.Fatalf("stale watermark: %v", err)
	}
	staleReader, err := mirrorstore.New(mirrorstore.Config{
		DB: db, Schema: schema, Group: row.Group, Kind: row.Kind, MaxLag: time.Second,
	}, func(_ context.Context, raw mirrorstore.RawRow) (string, error) { return raw.Name, nil })
	if err != nil {
		t.Fatalf("mirrorstore: %v", err)
	}
	if _, _, err := staleReader.ListPage(ctx, nil, 0, 10); err == nil {
		t.Fatal("a stale projection answered a List")
	}

	if err := writer.Delete(ctx, []Key{row.Key}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if count, err := writer.Count(ctx, row.Group, row.Kind); err != nil || count != 0 {
		t.Fatalf("count after delete = %d (%v)", count, err)
	}
}

func watermarkFor(row Row, at time.Time) readmodel.Watermark {
	return readmodel.Watermark{
		Group:       row.Group,
		Kind:        row.Kind,
		ObjectCount: 1,
		LastSweepAt: at,
		UpdatedAt:   at,
	}
}
