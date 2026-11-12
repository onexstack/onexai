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

package agentedge

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	agentstore "github.com/onexstack/onexai/internal/agent/store"
	"github.com/onexstack/onexai/internal/pkg/netguard"
	agentapi "github.com/onexstack/onexai/pkg/agentapi"
	"github.com/onexstack/onexai/pkg/media"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// stubRows answers an asset lookup with a fixed row, so the id-to-key hop can
// be tested without a database.
type stubRows struct {
	agentstore.AssetStore
	row *model.AssetM
	err error
}

func (s *stubRows) Get(_ context.Context, opts *where.Options) (*model.AssetM, error) {
	id, _ := opts.Filters["id"].(string)
	if s.err != nil {
		return nil, s.err
	}
	if s.row == nil || s.row.ID != id {
		return nil, agentstore.ErrNotFound
	}
	return s.row, nil
}

func newStore(t *testing.T) (*media.Bundle, media.Ref) {
	t.Helper()
	store, err := media.NewLocalStore(t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	bundle := media.NewBundle(store, nil)
	ref, err := bundle.Put(context.Background(), media.KindImage, "ou_a", "shot.png", pngBytes(t))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return bundle, ref
}

// TestInlineResolvesAStoredImage is the package's reason to exist: a reference
// becomes bytes the model can be given.
func TestInlineResolvesAStoredImage(t *testing.T) {
	bundle, ref := newStore(t)
	in := New(Deps{Bundle: bundle})

	out := in.Inline(context.Background(), []agentapi.Message{
		{Role: agentapi.RoleUser, Content: "看这个", Images: []media.Ref{ref}},
	})

	if len(out) != 1 || len(out[0].Images) != 1 {
		t.Fatalf("the image was dropped: %+v", out)
	}
	got := out[0].Images[0]
	if !strings.HasPrefix(got.URL, "data:image/png;base64,") {
		t.Fatalf("URL = %q, want a png data URL", got.URL)
	}
	if got.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", got.MimeType)
	}
	if _, ok := got.ModelReadyURL(); !ok {
		t.Error("the resolved reference is not one the engines will send")
	}
}

// TestInlineDoesNotTouchItsInput is the invariant the whole two-slice design
// rests on.
//
// The caller hands one slice to the engine and writes the other to the session
// store. If Inline resolved in place, the stored history would gain the data
// URL — which is the exact cost this feature exists to remove, reintroduced by
// an aliasing bug rather than by a decision.
func TestInlineDoesNotTouchItsInput(t *testing.T) {
	bundle, ref := newStore(t)
	in := New(Deps{Bundle: bundle})

	original := []agentapi.Message{{Role: agentapi.RoleUser, Content: "看这个", Images: []media.Ref{ref}}}
	before := original[0].Images[0].URL

	out := in.Inline(context.Background(), original)

	if original[0].Images[0].URL != before {
		t.Fatalf("the input's reference was rewritten: %q", original[0].Images[0].URL)
	}
	if strings.HasPrefix(original[0].Images[0].URL, "data:") {
		t.Fatal("the caller's own slice now holds bytes")
	}
	if out[0].Images[0].URL == before {
		t.Fatal("the output was not resolved")
	}
	// The slices must not share a backing array either: a later append to one
	// would otherwise overwrite the other.
	if &out[0].Images[0] == &original[0].Images[0] {
		t.Error("the output's image slice aliases the input's")
	}
}

// TestInlineResolvesThroughTheAssetRow covers the reference that carries only
// an id — which is what a stored session holds.
func TestInlineResolvesThroughTheAssetRow(t *testing.T) {
	bundle, ref := newStore(t)
	rows := &stubRows{row: &model.AssetM{
		ID: ref.AssetID, ObjectKey: ref.ObjectKey, MimeType: ref.MimeType,
	}}
	in := New(Deps{Bundle: bundle, Rows: rows})

	// The reference as a session would hold it: an id and nothing else.
	out := in.Inline(context.Background(), []agentapi.Message{
		{Role: agentapi.RoleUser, Images: []media.Ref{{AssetID: ref.AssetID}}},
	})
	if len(out[0].Images) != 1 || !strings.HasPrefix(out[0].Images[0].URL, "data:") {
		t.Fatalf("an id-only reference did not resolve: %+v", out[0].Images)
	}

	// With no row store at all, the same reference cannot be resolved — and the
	// message says so rather than reaching the model as a picture it never got.
	bare := New(Deps{Bundle: bundle})
	out = bare.Inline(context.Background(), []agentapi.Message{
		{Role: agentapi.RoleUser, Content: "看图", Images: []media.Ref{{AssetID: ref.AssetID}}},
	})
	if len(out[0].Images) != 0 {
		t.Errorf("an unresolvable reference was passed through: %+v", out[0].Images)
	}
	if !strings.Contains(out[0].Content, "未能读取") {
		t.Errorf("the message does not say an image was lost: %q", out[0].Content)
	}
}

// TestInlinePassesThroughAnAlreadyInlineImage pins that a data URL is not
// decoded and re-encoded: a history written by an earlier turn of this process
// carries its bytes already, and round-tripping them is work with no effect.
func TestInlinePassesThroughAnAlreadyInlineImage(t *testing.T) {
	bundle, _ := newStore(t)
	in := New(Deps{Bundle: bundle})

	const dataURL = "data:image/png;base64,iVBORw0KGgo="
	out := in.Inline(context.Background(), []agentapi.Message{
		{Role: agentapi.RoleUser, Images: []media.Ref{{URL: dataURL, MimeType: "image/png"}}},
	})
	if len(out[0].Images) != 1 || out[0].Images[0].URL != dataURL {
		t.Fatalf("an already-inline image was altered: %+v", out[0].Images)
	}
}

// TestInlineFetchesARemoteURL covers the reference that is neither in the store
// nor a data URL: a URL someone else served.
func TestInlineFetchesARemoteURL(t *testing.T) {
	body := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	in := New(Deps{Fetcher: netguard.NewFetcher(netguard.Options{AllowPrivateHosts: true})})
	out := in.Inline(context.Background(), []agentapi.Message{
		{Role: agentapi.RoleUser, Images: []media.Ref{{URL: srv.URL + "/a.png"}}},
	})
	if len(out[0].Images) != 1 {
		t.Fatalf("the remote image was dropped: %+v", out[0].Images)
	}
	if !strings.HasPrefix(out[0].Images[0].URL, "data:image/png;base64,") {
		t.Errorf("URL = %q, want a png data URL", out[0].Images[0].URL)
	}
}

// TestInlineServesItsOwnURLFromDisk pins that this service does not fetch its
// own objects over the network. The result is the same either way, so what is
// being asserted is the path: a fetch would spend a socket and a request's
// worth of server resources to read a file this process can open.
func TestInlineServesItsOwnURLFromDisk(t *testing.T) {
	bundle, ref := newStore(t)
	// A fetcher that would fail loudly, so a network path shows up as a
	// dropped image rather than as a silently slower success.
	in := New(Deps{Bundle: bundle, Fetcher: netguard.NewFetcher(netguard.Options{})})

	out := in.Inline(context.Background(), []agentapi.Message{
		{Role: agentapi.RoleUser, Images: []media.Ref{{URL: ref.URL, AssetID: ref.AssetID}}},
	})
	if len(out[0].Images) != 1 || !strings.HasPrefix(out[0].Images[0].URL, "data:") {
		t.Fatalf("a self-served URL was not read from the store: %+v", out[0].Images)
	}
}

// TestInlinePrefersTheSelfServedURLOverTheRow pins the read order.
//
// A reference from the chat service carries an id and a URL and no key, so
// without this the hot path — every image, every turn — costs a row read to
// learn a key that the URL already spells out. The counting store is what makes
// the difference visible; the bytes are identical either way, so only the
// number of reads can distinguish the two implementations.
func TestInlinePrefersTheSelfServedURLOverTheRow(t *testing.T) {
	bundle, ref := newStore(t)
	rows := &countingRows{stubRows: stubRows{row: &model.AssetM{
		ID: ref.AssetID, ObjectKey: ref.ObjectKey, MimeType: ref.MimeType,
	}}}
	in := New(Deps{Bundle: bundle, Rows: rows})

	// The shape the chat service sends: an id and a URL, no key.
	out := in.Inline(context.Background(), []agentapi.Message{
		{Role: agentapi.RoleUser, Images: []media.Ref{{AssetID: ref.AssetID, URL: ref.URL, MimeType: ref.MimeType}}},
	})
	if len(out[0].Images) != 1 || !strings.HasPrefix(out[0].Images[0].URL, "data:") {
		t.Fatalf("the image did not resolve: %+v", out[0].Images)
	}
	if rows.calls != 0 {
		t.Errorf("the row store was read %d times for a URL that names its own object", rows.calls)
	}
}

// TestInlineDropsWhatItCannotReadAndSaysSo pins the degradation.
//
// Refusing the turn would lose the question along with the picture. Answering
// silently would produce an answer that reads as if the model had seen the
// image and found it unremarkable — which is worse, because nobody looks.
func TestInlineDropsWhatItCannotReadAndSaysSo(t *testing.T) {
	bundle, _ := newStore(t)
	in := New(Deps{Bundle: bundle})

	out := in.Inline(context.Background(), []agentapi.Message{
		{Role: agentapi.RoleUser, Content: "这是什么", Images: []media.Ref{{AssetID: "img_missing"}}},
	})
	if len(out[0].Images) != 0 {
		t.Errorf("an unreadable image survived: %+v", out[0].Images)
	}
	if !strings.Contains(out[0].Content, "这是什么") {
		t.Errorf("the question was lost: %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "未能读取") {
		t.Errorf("the note is missing: %q", out[0].Content)
	}

	// Several unreadable images are counted, because "one of the three failed"
	// and "all three failed" call for different confidence in the answer.
	out = in.Inline(context.Background(), []agentapi.Message{
		{Role: agentapi.RoleUser, Content: "看", Images: []media.Ref{
			{AssetID: "m1"}, {AssetID: "m2"}, {AssetID: "m3"},
		}},
	})
	if !strings.Contains(out[0].Content, "3") {
		t.Errorf("the note does not count the losses: %q", out[0].Content)
	}
}

// TestInlineWithoutAStoreIsANoOp pins the disabled deployment: nothing is
// configured, so the messages come back as they were rather than being emptied.
func TestInlineWithoutAStoreIsANoOp(t *testing.T) {
	in := New(Deps{})
	if in.Available() {
		t.Error("an empty Deps reports itself available")
	}
	msgs := []agentapi.Message{{Role: agentapi.RoleUser, Images: []media.Ref{{AssetID: "img_a"}}}}
	out := in.Inline(context.Background(), msgs)
	if len(out[0].Images) != 1 {
		t.Errorf("a disabled inliner emptied the messages: %+v", out[0])
	}
}

// TestInlineCachesResolvedBytes pins that the same image is read once.
//
// It is read on every turn of a conversation, so a store read per turn per
// image is the cost this cache exists to remove. The count comes from a row
// store that counts its lookups.
func TestInlineCachesResolvedBytes(t *testing.T) {
	bundle, ref := newStore(t)
	rows := &countingRows{stubRows: stubRows{row: &model.AssetM{
		ID: ref.AssetID, ObjectKey: ref.ObjectKey, MimeType: ref.MimeType,
	}}}
	in := New(Deps{Bundle: bundle, Rows: rows})

	msgs := []agentapi.Message{{Role: agentapi.RoleUser, Images: []media.Ref{{AssetID: ref.AssetID}}}}
	first := in.Inline(context.Background(), msgs)
	second := in.Inline(context.Background(), msgs)

	if first[0].Images[0].URL != second[0].Images[0].URL {
		t.Fatal("the two resolutions disagree")
	}
	if rows.calls != 1 {
		t.Errorf("the store was read %d times, want 1", rows.calls)
	}
}

type countingRows struct {
	stubRows
	calls int
}

func (s *countingRows) Get(ctx context.Context, opts *where.Options) (*model.AssetM, error) {
	s.calls++
	return s.stubRows.Get(ctx, opts)
}

// TestInlineRefsReportsWhatItCouldNotRead pins the bare-list form the tool path
// uses: it returns the losses as a count rather than a message to edit.
func TestInlineRefsReportsWhatItCouldNotRead(t *testing.T) {
	bundle, ref := newStore(t)
	in := New(Deps{Bundle: bundle})

	got, missing := in.InlineRefs(context.Background(), []media.Ref{ref, {AssetID: "img_gone"}})
	if len(got) != 1 || missing != 1 {
		t.Fatalf("got %d refs and %d missing, want 1 and 1", len(got), missing)
	}
	if _, ok := got[0].ModelReadyURL(); !ok {
		t.Error("the resolved reference is not one the engines will send")
	}
}

// TestObjectKeyFromURL pins the recognition of this service's own read path,
// including the shapes that must NOT be treated as one.
func TestObjectKeyFromURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "/media/images/u/img_a/a.png", want: "images/u/img_a/a.png", ok: true},
		{in: "/media/images/u/img_a/a.png?s=sig", want: "images/u/img_a/a.png", ok: true},
		{in: "/media/", ok: false},
		{in: "https://x/media/a.png", ok: false},
		{in: "/media/../../etc/passwd", ok: false},
	}
	for _, tt := range tests {
		got, ok := objectKeyFromURL(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("objectKeyFromURL(%q) = %q,%v; want %q,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// TestPickChoosesAUsableType pins the last resort: an image whose type is not
// known must still produce a type the model endpoint accepts, because some
// endpoints reject an octet-stream data URL outright.
func TestPickChoosesAUsableType(t *testing.T) {
	png := pngBytes(t)
	if got := pickMime("image/jpeg", "image/png", png); got != "image/jpeg" {
		t.Errorf("pickMime preferred %q over the recorded type", got)
	}
	if got := pickMime("", "image/webp", png); got != "image/webp" {
		t.Errorf("pickMime = %q, want the fetched type", got)
	}
	if got := pickMime("application/octet-stream", "", png); got != "image/png" {
		t.Errorf("pickMime = %q, want the sniffed type", got)
	}
	if got := pickMime("", "", []byte("not an image")); got != "image/png" {
		t.Errorf("pickMime = %q, want a usable fallback", got)
	}
}

// errBoom stands in for a store that is down.
var errBoom = errors.New("store is down")

// TestInlineReportsAStoreFailureAsUnreadable pins that a backend failure and an
// absent image are the same answer to the model — it did not get the picture —
// rather than one of them ending the turn.
func TestInlineReportsAStoreFailureAsUnreadable(t *testing.T) {
	bundle, _ := newStore(t)
	in := New(Deps{Bundle: bundle, Rows: &stubRows{err: errBoom}})

	out := in.Inline(context.Background(), []agentapi.Message{
		{Role: agentapi.RoleUser, Content: "看图", Images: []media.Ref{{AssetID: "img_a"}}},
	})
	if len(out[0].Images) != 0 {
		t.Errorf("an image survived a store failure: %+v", out[0].Images)
	}
	if !strings.Contains(out[0].Content, "未能读取") {
		t.Errorf("the note is missing: %q", out[0].Content)
	}
}

// TestCacheKeyDistinguishesReferences pins that two different images do not
// share a cache entry — which would serve one picture where another was asked
// for, silently and only for whichever was seen second.
func TestCacheKeyDistinguishesReferences(t *testing.T) {
	a := media.Ref{AssetID: "img_a"}.CacheKey()
	b := media.Ref{AssetID: "img_b"}.CacheKey()
	c := media.Ref{ObjectKey: "images/u/img_a/a.png"}.CacheKey()
	u1 := media.Ref{URL: "https://x/a.png"}.CacheKey()
	u2 := media.Ref{URL: "https://x/b.png"}.CacheKey()

	seen := map[string]string{}
	for name, key := range map[string]string{"a": a, "b": b, "c": c, "u1": u1, "u2": u2} {
		if prev, dup := seen[key]; dup {
			t.Errorf("%s and %s share the cache key %q", prev, name, key)
		}
		seen[key] = name
	}
	if (media.Ref{}).CacheKey() != "" {
		t.Error("a reference that names nothing has a cache key")
	}
	// A data URL is hashed rather than used whole, so a megabyte-long key does
	// not become the map's own memory.
	long := media.Ref{URL: "data:image/png;base64," + strings.Repeat("A", 5000)}
	if k := long.CacheKey(); len(k) > 64 {
		t.Errorf("the cache key for a data URL is %d bytes long", len(k))
	}
}

// sizedPNG renders a solid PNG of the given size.
func sizedPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// TestInlineShrinksOnlyWhenAsked is the switch the deployment owns.
//
// Shrinking is lossy and the detail it costs first is small text, which is
// exactly what a screenshot sent to this bot is for. So the default sends the
// original bytes, and a ceiling is applied only when one was configured.
func TestInlineShrinksOnlyWhenAsked(t *testing.T) {
	store, err := media.NewLocalStore(t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	bundle := media.NewBundle(store, nil)
	big := sizedPNG(t, 200, 100) // 20000 pixels, comfortably over a small ceiling
	ref, err := bundle.Put(context.Background(), media.KindImage, "ou_a", "big.png", big)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	inlined := func(t *testing.T, maxPixels int) []byte {
		t.Helper()
		in := New(Deps{Bundle: bundle, MaxPixels: maxPixels})
		out := in.Inline(context.Background(), []agentapi.Message{
			{Role: agentapi.RoleUser, Images: []media.Ref{ref}},
		})
		if len(out[0].Images) != 1 {
			t.Fatalf("the image was dropped: %+v", out[0].Images)
		}
		data, err := media.DecodeDataURL(out[0].Images[0].URL)
		if err != nil {
			t.Fatalf("the inline URL does not decode: %v", err)
		}
		return data
	}

	t.Run("no ceiling configured", func(t *testing.T) {
		got := inlined(t, 0)
		if !bytes.Equal(got, big) {
			t.Errorf("the bytes changed with no ceiling: %d in, %d out", len(big), len(got))
		}
	})

	t.Run("a ceiling configured", func(t *testing.T) {
		got := inlined(t, 2000)
		w, h := media.ImageSize(got)
		if w*h > 2000 {
			t.Errorf("the model was sent %dx%d = %d pixels, over the 2000 configured", w, h, w*h)
		}
		if w/h != 2 {
			t.Errorf("the shape changed: %dx%d from 200x100", w, h)
		}
		if len(got) >= len(big) {
			t.Errorf("nothing was shrunk: %d bytes in, %d out", len(big), len(got))
		}
	})

	t.Run("the stored bytes are untouched", func(t *testing.T) {
		// The ceiling applies to what the model is shown, never to what is
		// stored: a later turn at a higher ceiling must still be able to send
		// the original, and the asset row's size has to keep describing the
		// object.
		inlined(t, 2000)
		back, _, err := bundle.OpenBytes(context.Background(), ref)
		if err != nil {
			t.Fatalf("OpenBytes: %v", err)
		}
		if !bytes.Equal(back, big) {
			t.Error("the stored object was rewritten by a model-edge ceiling")
		}
	})
}
