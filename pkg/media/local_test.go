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

package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestBundle(t *testing.T) *Bundle {
	t.Helper()
	store, err := NewLocalStore(t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	return NewBundle(store, nil)
}

func TestBundlePutRoundTrip(t *testing.T) {
	b := newTestBundle(t)
	ctx := context.Background()
	data := testPNG(t, 4, 6)

	ref, err := b.Put(ctx, KindImage, "ou_abc", "screenshot.png", data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if !strings.HasPrefix(ref.AssetID, assetIDPrefix) {
		t.Errorf("asset id %q does not carry the %q prefix", ref.AssetID, assetIDPrefix)
	}
	if ref.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", ref.MimeType)
	}
	if ref.Width != 4 || ref.Height != 6 {
		t.Errorf("dimensions = %dx%d, want 4x6", ref.Width, ref.Height)
	}
	if ref.Size != int64(len(data)) {
		t.Errorf("Size = %d, want %d", ref.Size, len(data))
	}
	if !strings.HasPrefix(ref.URL, PublicPath) {
		t.Errorf("URL = %q, want a %s path", ref.URL, PublicPath)
	}

	got, contentType, err := b.OpenBytes(ctx, ref)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("OpenBytes returned different bytes than were stored")
	}
	if contentType != "image/png" {
		t.Errorf("content type = %q, want image/png", contentType)
	}

	obj, err := b.Stat(ctx, ref)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if obj.Size != int64(len(data)) {
		t.Errorf("Stat size = %d, want %d", obj.Size, len(data))
	}

	if err := b.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := b.Stat(ctx, ref); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Stat after Delete = %v, want ErrObjectNotFound", err)
	}
	// Deleting again is not an error: the outcome is the same either way, and
	// a caller retrying after a partial failure must not be told it failed.
	if err := b.Delete(ctx, ref); err != nil {
		t.Errorf("second Delete returned %v, want nil", err)
	}
}

// TestBundlePutRefusesUnsupported pins that the rejections happen before
// anything is written. A check made after the bytes have landed cannot un-land
// them.
func TestBundlePutRefusesUnsupported(t *testing.T) {
	b := newTestBundle(t)
	ctx := context.Background()

	if _, err := b.Put(ctx, "nonsense-kind", "u", "a.png", testPNG(t, 1, 1)); !errors.Is(err, ErrUnsupportedKind) {
		t.Errorf("Put with an unknown kind = %v, want ErrUnsupportedKind", err)
	}
	if _, err := b.Put(ctx, KindImage, "u", "a.png", []byte("<html>hi</html>")); !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("Put of an HTML file = %v, want ErrUnsupportedType", err)
	}
	if _, err := b.Put(ctx, KindImage, "u", "a.png", nil); !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("Put of nothing = %v, want ErrUnsupportedType", err)
	}

	// Nothing was written, so the store directory is still empty.
	store := b.Store().(*LocalStore)
	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("store holds %d entries after only refused uploads, want 0", len(entries))
	}
}

// TestBundlePutSeparatesSameNames is the reason the asset id is a directory of
// its own: two uploads called IMG_0001.jpg must not overwrite each other, or
// the first row would go on serving the second's photo.
func TestBundlePutSeparatesSameNames(t *testing.T) {
	b := newTestBundle(t)
	ctx := context.Background()

	first := testPNG(t, 3, 3)
	second := testPNG(t, 5, 5)

	r1, err := b.Put(ctx, KindImage, "ou_abc", "IMG_0001.png", first)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	r2, err := b.Put(ctx, KindImage, "ou_abc", "IMG_0001.png", second)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if r1.ObjectKey == r2.ObjectKey {
		t.Fatalf("both uploads got the key %q", r1.ObjectKey)
	}

	got1, _, err := b.OpenBytes(ctx, r1)
	if err != nil {
		t.Fatalf("OpenBytes(first): %v", err)
	}
	if !bytes.Equal(got1, first) {
		t.Error("the first upload now serves the second's bytes")
	}
}

func TestBundleWithoutStoreRefuses(t *testing.T) {
	b := NewBundle(nil, nil)
	if b.Configured() {
		t.Error("a bundle with no store reports itself configured")
	}
	if _, err := b.Put(context.Background(), KindImage, "u", "a.png", testPNG(t, 1, 1)); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Put with no store = %v, want ErrNotConfigured", err)
	}
}

// TestBundleKeyForRefusesBareURL pins the distinction between "an object in
// this store" and "a URL somewhere". A ref that names only a URL has no key to
// derive, and guessing one would fetch the wrong thing or nothing.
func TestBundleKeyForRefusesBareURL(t *testing.T) {
	b := newTestBundle(t)
	_, err := b.Open(context.Background(), Ref{URL: "https://cdn.example.com/a.png"})
	if !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Open of a URL-only ref = %v, want ErrObjectNotFound", err)
	}
	_, err = b.Open(context.Background(), Ref{AssetID: "img_a"})
	if err == nil {
		t.Error("Open of an asset-id-only ref succeeded; it has no key in this store")
	}
}

func TestLocalStoreRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLocalStore(dir, "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	ctx := context.Background()

	// A file outside the store, which a successful traversal would reach.
	outside := filepath.Join(filepath.Dir(dir), "secret.png")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, key := range []string{"../secret.png", "images/../../secret.png", "/etc/passwd", ""} {
		if _, err := store.Open(ctx, key); err == nil {
			t.Errorf("Open(%q) succeeded, want a refusal", key)
		}
		if _, err := store.Stat(ctx, key); err == nil {
			t.Errorf("Stat(%q) succeeded, want a refusal", key)
		}
	}

	for _, key := range []string{"../secret.png", "images/../../secret.png", "/etc/passwd"} {
		if err := store.Delete(ctx, key); err == nil {
			t.Errorf("Delete(%q) succeeded, want a refusal", key)
		}
	}

	// The empty key is a different case from a traversal: there is nothing at
	// it, and deleting nothing has the same outcome as deleting it. It is a
	// no-op rather than an error, the same rule a missing object gets.
	if err := store.Delete(ctx, ""); err != nil {
		t.Errorf("Delete(\"\") = %v, want nil", err)
	}

	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the file outside the store was removed: %v", err)
	}
}

// TestLocalStoreDeleteEmptiesTheTree pins that deleting an asset does not leave
// its directories behind.
//
// An owner directory holding nothing is the store telling a reader that
// something is there. It also matters for the operator who has to size a disk:
// a directory per deleted asset is a listing of what used to exist, and it
// grows with every removal rather than with what is stored.
//
// The owner's directory goes only when it is empty — another asset under the
// same owner must keep it.
func TestLocalStoreDeleteEmptiesTheTree(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLocalStore(dir, "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	b := NewBundle(store, nil)
	ctx := context.Background()

	first, err := b.Put(ctx, KindImage, "ou_a", "one.png", testPNG(t, 2, 2))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	second, err := b.Put(ctx, KindImage, "ou_a", "two.png", testPNG(t, 3, 3))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	ownerDir := filepath.Join(dir, filepath.Dir(filepath.Dir(first.ObjectKey)))

	if err := b.Delete(ctx, first); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(ownerDir); err != nil {
		t.Fatalf("the owner directory went while it still held an asset: %v", err)
	}

	if err := b.Delete(ctx, second); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if _, err := os.Stat(ownerDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the owner directory survived its last asset being deleted")
	}
	// The prefix directory itself is left: it is the store's own structure, not
	// something an asset created. The name is the kind's prefix, which is not
	// the kind.
	prefix, ok := DefaultKinds()[KindImage]
	if !ok {
		t.Fatalf("no rules for %s", KindImage)
	}
	if _, err := os.Stat(filepath.Join(dir, prefix.Prefix)); err != nil {
		t.Errorf("the prefix directory was removed: %v", err)
	}
}

func TestLocalStoreServeHTTP(t *testing.T) {
	store, err := NewLocalStore(t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	ctx := context.Background()
	data := testPNG(t, 2, 2)

	key := "images/ou_abc/img_1/a.png"
	if _, err := store.Put(ctx, PutRequest{ObjectKey: key, ContentType: "image/png"}, bytes.NewReader(data)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	fetch := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		store.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}

	if rec := fetch(http.MethodGet, PublicPath+key); rec.Code != http.StatusOK {
		t.Errorf("GET of a stored object = %d, want 200", rec.Code)
	}
	if rec := fetch(http.MethodGet, PublicPath+"images/ou_abc/img_1/missing.png"); rec.Code != http.StatusNotFound {
		t.Errorf("GET of a missing object = %d, want 404", rec.Code)
	}
	if rec := fetch(http.MethodGet, PublicPath+"../../etc/passwd"); rec.Code != http.StatusBadRequest {
		t.Errorf("GET of a traversal path = %d, want 400", rec.Code)
	}
	if rec := fetch(http.MethodPost, PublicPath+key); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

// TestLocalStoreSigning pins both halves of the signature: a signed URL is
// served, and a key presented with the wrong signature is not.
func TestLocalStoreSigning(t *testing.T) {
	store, err := NewLocalStore(t.TempDir(), "", []byte("shared-secret"))
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	ctx := context.Background()
	key := "images/ou_abc/img_1/a.png"
	if _, err := store.Put(ctx, PutRequest{ObjectKey: key, ContentType: "image/png"}, bytes.NewReader(testPNG(t, 1, 1))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	url := store.URL(key)
	if !strings.Contains(url, "?s=") {
		t.Fatalf("URL %q carries no signature", url)
	}
	// The signature is over the key alone, so it is stable — which is what
	// lets a URL be written to a session and read back later.
	if again := store.URL(key); again != url {
		t.Errorf("URL is not stable: %q then %q", url, again)
	}

	rec := httptest.NewRecorder()
	store.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET of a signed object = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	store.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PublicPath+key+"?s=forged", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET with a forged signature = %d, want 403", rec.Code)
	}
}

func TestOpenBytesStopsAtTheRecordedSize(t *testing.T) {
	// A store that reports less than it holds is the case the bound exists
	// for: the bytes are about to be held in memory and base64-expanded.
	store := &lyingStore{data: []byte("0123456789"), size: 4}
	_, _, err := OpenBytes(context.Background(), store, "k")
	if err == nil {
		t.Fatal("OpenBytes read past the recorded size")
	}
}

// lyingStore reports a size smaller than the bytes it returns.
type lyingStore struct {
	data []byte
	size int64
}

func (s *lyingStore) Put(context.Context, PutRequest, io.Reader) (Object, error) {
	return Object{}, nil
}
func (s *lyingStore) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func (s *lyingStore) Stat(context.Context, string) (Object, error) {
	return Object{ObjectKey: "k", Size: s.size}, nil
}
func (s *lyingStore) Delete(context.Context, string) error { return nil }
func (s *lyingStore) URL(string) string                    { return "" }
func (s *lyingStore) Name() string                         { return "lying" }
