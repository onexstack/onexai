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

package asset

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/ptr"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	agentv1 "github.com/onexstack/onexai/pkg/api/agent/v1"
	"github.com/onexstack/onexai/pkg/media"
)

func pngB64(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// fakeRows is an in-memory asset table. Only the calls this package makes are
// implemented; the rest of the interface is left nil so a method this code
// started using would panic here rather than in production.
type fakeRows struct {
	agentstore.AssetStore
	rows       map[string]*model.AssetM
	createErr  error
	creates    int
	deletes    int
	lastDelete string
}

func newFakeRows() *fakeRows { return &fakeRows{rows: map[string]*model.AssetM{}} }

func (f *fakeRows) Create(_ context.Context, a *model.AssetM) error {
	f.creates++
	if f.createErr != nil {
		return f.createErr
	}
	f.rows[a.ID] = ptr.Clone(a)
	return nil
}

func (f *fakeRows) Get(_ context.Context, opts *where.Options) (*model.AssetM, error) {
	id, _ := opts.Filters["id"].(string)
	row, ok := f.rows[id]
	if !ok {
		return nil, agentstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeRows) GetByDigest(_ context.Context, ownerID, digest string) (*model.AssetM, error) {
	for _, row := range f.rows {
		if row.OwnerID == ownerID && row.Sha256 == digest {
			return row, nil
		}
	}
	return nil, agentstore.ErrNotFound
}

func (f *fakeRows) Delete(_ context.Context, opts *where.Options) error {
	id, _ := opts.Filters["id"].(string)
	f.deletes++
	f.lastDelete = id
	delete(f.rows, id)
	return nil
}

func newBiz(t *testing.T, rows agentstore.AssetStore) (*assetBiz, *media.Bundle) {
	t.Helper()
	store, err := media.NewLocalStore(t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	bundle := media.NewBundle(store, nil)
	return New(bundle, rows, nil, 0), bundle
}

func createReq(t *testing.T, owner, data string) *agentv1.CreateAssetRequest {
	t.Helper()
	return &agentv1.CreateAssetRequest{
		Kind: "image", OwnerID: owner, Filename: "a.png", Data: data, Source: "upload",
	}
}

// TestCreateStoresAndDescribesAnImage pins the whole of one upload: bytes in
// the store, a row describing them, and a reference a caller can carry.
func TestCreateStoresAndDescribesAnImage(t *testing.T) {
	rows := newFakeRows()
	b, _ := newBiz(t, rows)
	ctx := context.Background()

	out, err := b.Create(ctx, createReq(t, "go.kong", pngB64(t)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.GetAssetId() == "" || out.GetMimeType() != "image/png" {
		t.Fatalf("the asset is not usable: %+v", out)
	}
	if out.GetWidth() != 4 || out.GetHeight() != 4 {
		t.Errorf("dimensions = %dx%d, want 4x4", out.GetWidth(), out.GetHeight())
	}
	if out.GetSize() == 0 {
		t.Error("the size was not recorded")
	}

	// The bytes come back.
	got, contentType, err := b.Content(ctx, out.GetAssetId())
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if contentType != "image/png" || len(got) == 0 {
		t.Errorf("Content returned %d bytes of %q", len(got), contentType)
	}
}

// TestCreateIsIdempotentByDigest pins that the same file twice costs one row
// and one object. The check is per owner, which is what the next test is about.
func TestCreateIsIdempotentByDigest(t *testing.T) {
	rows := newFakeRows()
	b, _ := newBiz(t, rows)
	ctx := context.Background()
	data := pngB64(t)

	first, err := b.Create(ctx, createReq(t, "go.kong", data))
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := b.Create(ctx, createReq(t, "go.kong", data))
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}

	if first.GetAssetId() != second.GetAssetId() {
		t.Errorf("the same bytes under one owner produced two assets: %s and %s",
			first.GetAssetId(), second.GetAssetId())
	}
	if rows.creates != 1 {
		t.Errorf("the table was written %d times, want 1", rows.creates)
	}
}

// TestCreateKeepsOwnersApart is the security property of the dedup.
//
// Two people uploading the same picture must each get their own asset: the
// identity on the row is what a later read is checked against, so returning one
// person the row belonging to another would let them read a stranger's upload
// back as themselves. Identical bytes are not a reason to share an identity.
func TestCreateKeepsOwnersApart(t *testing.T) {
	rows := newFakeRows()
	b, _ := newBiz(t, rows)
	ctx := context.Background()
	data := pngB64(t)

	mine, err := b.Create(ctx, createReq(t, "go.kong", data))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	theirs, err := b.Create(ctx, createReq(t, "someone.else", data))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if mine.GetAssetId() == theirs.GetAssetId() {
		t.Fatalf("two owners share the asset %s", mine.GetAssetId())
	}
	if rows.creates != 2 {
		t.Errorf("the table was written %d times, want 2", rows.creates)
	}
}

// TestCreateRemovesTheBytesWhenTheRowFails pins that a failed write does not
// leave an object behind.
//
// A file nothing points at is a leak only a sweep would find, and a sweep looks
// at rows — so an orphan is invisible for as long as it exists.
func TestCreateRemovesTheBytesWhenTheRowFails(t *testing.T) {
	rows := newFakeRows()
	rows.createErr = errors.New("database is down")
	b, bundle := newBiz(t, rows)
	ctx := context.Background()

	if _, err := b.Create(ctx, createReq(t, "go.kong", pngB64(t))); err == nil {
		t.Fatal("Create succeeded with a failing table")
	}

	// Nothing is addressable: the id was never returned, so the only way to
	// check is that the store holds no objects at all.
	store := bundle.Store().(*media.LocalStore)
	entries, err := countFiles(t, store.Dir())
	if err != nil {
		t.Fatalf("count objects: %v", err)
	}
	if entries != 0 {
		t.Errorf("the store holds %d files after a failed write, want 0", entries)
	}
}

// TestCreateRefusesBeforeWriting pins that the checks happen first. A rejection
// made after the bytes have landed cannot un-land them.
func TestCreateRefusesBeforeWriting(t *testing.T) {
	rows := newFakeRows()
	b, bundle := newBiz(t, rows)
	ctx := context.Background()

	tests := []struct {
		name string
		req  *agentv1.CreateAssetRequest
	}{
		{"no data", &agentv1.CreateAssetRequest{Kind: "image", OwnerID: "u", Data: ""}},
		{"not base64", &agentv1.CreateAssetRequest{Kind: "image", OwnerID: "u", Data: "!!!!"}},
		{"an unknown kind", &agentv1.CreateAssetRequest{Kind: "sausage", OwnerID: "u", Data: pngB64(t)}},
		{
			"a file that is not an image",
			&agentv1.CreateAssetRequest{
				Kind: "image", OwnerID: "u",
				Data: base64.StdEncoding.EncodeToString([]byte("<html>hello</html>")),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := b.Create(ctx, tt.req); err == nil {
				t.Fatalf("Create accepted %s", tt.name)
			}
		})
	}

	if rows.creates != 0 {
		t.Errorf("the table was written %d times for refused uploads", rows.creates)
	}
	store := bundle.Store().(*media.LocalStore)
	if n, err := countFiles(t, store.Dir()); err != nil || n != 0 {
		t.Errorf("the store holds %d files after only refusals (err %v)", n, err)
	}
}

// TestDeleteRemovesTheRowAndTheBytes pins that deletion is complete. A row
// without its object serves a 404 through the API and wastes the file; an
// object without its row is a leak nothing can find.
func TestDeleteRemovesTheRowAndTheBytes(t *testing.T) {
	rows := newFakeRows()
	b, bundle := newBiz(t, rows)
	ctx := context.Background()

	created, err := b.Create(ctx, createReq(t, "go.kong", pngB64(t)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	store := bundle.Store().(*media.LocalStore)
	if n, _ := countFiles(t, store.Dir()); n != 1 {
		t.Fatalf("the store holds %d files before the delete, want 1", n)
	}

	if err := b.Delete(ctx, created.GetAssetId()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if rows.deletes != 1 || rows.lastDelete != created.GetAssetId() {
		t.Errorf("the row was not deleted: deletes=%d id=%q", rows.deletes, rows.lastDelete)
	}
	if n, _ := countFiles(t, store.Dir()); n != 0 {
		t.Errorf("the store holds %d files after the delete, want 0", n)
	}

	// Deleting again is a refusal, not a silent success: the row is already
	// gone, and a caller retrying after a partial failure needs to be told.
	if err := b.Delete(ctx, created.GetAssetId()); err == nil {
		t.Error("deleting a missing asset succeeded")
	}
}

// TestUnconfiguredRefusesEverything pins the disabled deployment. "Nowhere to
// put an image" is a configuration state, and every operation says so rather
// than failing somewhere deeper.
func TestUnconfiguredRefusesEverything(t *testing.T) {
	b := New(media.NewBundle(nil, nil), newFakeRows(), nil, 0)
	if b.Configured() {
		t.Error("a bundle with no store reports itself configured")
	}
	ctx := context.Background()

	if _, err := b.Create(ctx, createReq(t, "u", pngB64(t))); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Create = %v, want ErrNotConfigured", err)
	}
	if _, _, err := b.Content(ctx, "img_a"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Content = %v, want ErrNotConfigured", err)
	}
}

// TestStoreKeepsTheRegisteredKind pins that the kind reaches the key, so a
// generated image and an uploaded one are browsable apart.
func TestStoreKeepsTheRegisteredKind(t *testing.T) {
	rows := newFakeRows()
	b, _ := newBiz(t, rows)

	ref, err := b.Store(context.Background(), media.KindGenerated, "go.kong", "drawn.png",
		media.SourceGenerated, mustDecode(t, pngB64(t)))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	kind, ok := b.bundle.KindsFor(media.KindGenerated)
	if !ok {
		t.Fatal("the generated kind is not registered")
	}
	if !bytes.HasPrefix([]byte(ref.ObjectKey), []byte(kind.Prefix)) {
		t.Errorf("key %q does not sit under the %q prefix", ref.ObjectKey, kind.Prefix)
	}
	row := rows.rows[ref.AssetID]
	if row == nil || row.Kind != media.KindGenerated || row.Source != media.SourceGenerated {
		t.Errorf("the row lost the kind or the source: %+v", row)
	}
}

func mustDecode(t *testing.T, b64 string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return data
}

// countFiles counts the objects in a store directory.
//
// It walks rather than reading one level, because an object lives under
// <prefix>/<owner>/<asset>/<name> and a test that only counted the first level
// would report zero while files sat two directories down.
func countFiles(t *testing.T, root string) (int, error) {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	return n, err
}

// TestRetentionStampsADeadline pins where the expiry comes from.
//
// It is written when the object is stored rather than computed by the collector,
// so raising or lowering the retention applies to what comes next instead of
// reaching back and expiring images that were kept under the old policy. A
// settings change that silently deletes history is worse than one that applies
// to the future.
func TestRetentionStampsADeadline(t *testing.T) {
	rows := newFakeRows()
	store, err := media.NewLocalStore(t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	b := New(media.NewBundle(store, nil), rows, nil, time.Hour)
	ctx := context.Background()

	out, err := b.Create(ctx, createReq(t, "go.kong", pngB64(t)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	row := rows.rows[out.GetAssetId()]
	if row == nil {
		t.Fatal("no row was written")
	}
	if row.ExpiresAt == nil {
		t.Fatal("a retention was configured and no deadline was stamped")
	}
	// About an hour from now, not exactly: the assertion is that it is a real
	// deadline and not the epoch.
	if d := time.Until(*row.ExpiresAt); d < 55*time.Minute || d > 65*time.Minute {
		t.Errorf("the deadline is %v away, want about an hour", d)
	}

	// With no retention the row has no expiry, which is what the collector reads
	// as "never".
	b2 := New(media.NewBundle(store, nil), newFakeRows(), nil, 0)
	out2, err := b2.Create(ctx, createReq(t, "go.kong", pngB64(t)))
	if err != nil {
		t.Fatalf("Create without retention: %v", err)
	}
	rows2 := b2.store.(*fakeRows)
	if r := rows2.rows[out2.GetAssetId()]; r == nil || r.ExpiresAt != nil {
		t.Errorf("a row without a retention carries a deadline: %+v", r)
	}
}
