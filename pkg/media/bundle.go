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
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// anonymousOwner is the owner directory used when a caller could not name one.
//
// It is a fallback rather than a refusal because a Lark image can arrive from a
// sender the org chart does not resolve, and refusing to store it would drop a
// message the user sent. The objects stay distinct — the asset id is the
// directory below this one — so the only thing lost is the grouping that would
// have made a per-user quota possible for those objects.
const anonymousOwner = "anon"

// assetIDPrefix marks an asset id in a key and in a URL.
//
// It is not decoration: `ls images/` and a glance at a log line both read
// better when an id says what kind of thing it is, and it makes an id copied
// out of a key recognisable when it turns up in a Ref on its own.
const assetIDPrefix = "img_"

// NewAssetID returns a fresh asset id.
//
// The shape is the prefix and 32 hex characters of a UUIDv4. It is generated
// here rather than by the asset table's owner because the object key is built
// from the id before the row exists — the key needs the id, the row would
// otherwise need the insert, and a row written before there are bytes has a key
// that points at nothing.
//
// The characters are all inside the set a key segment accepts, so an id can be
// used in a key without escaping, and the randomness is what makes a stored
// object unguessable — which is what the public read path relies on.
func NewAssetID() string {
	return assetIDPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// Bundle ties a store to the kind rules and the URL policy.
//
// Services hold a Bundle, never a Store. That is the seam: one thing in the
// process knows which driver is configured, and a caller that reached past the
// bundle to the store would have to be handed the store too — at which point
// two things in the process know how storage is configured, and they can
// disagree.
type Bundle struct {
	store Store
	kinds map[string]Kinds
	// newID is a field so a test can make ids predictable. It is the only
	// source of randomness in a key, so it is the only thing a key assertion
	// would otherwise have to mock out from underneath.
	newID func() string
}

// NewBundle wraps a store with the kind rules.
//
// A nil store yields a bundle that refuses every operation with
// ErrNotConfigured rather than panicking, because "this deployment has no
// storage" is a configuration a service must be able to start with — image
// upload disabled is a valid deployment, and a nil dereference at the first
// upload is not how it should find out.
func NewBundle(store Store, kinds map[string]Kinds) *Bundle {
	if kinds == nil {
		kinds = DefaultKinds()
	}
	return &Bundle{
		store: store,
		kinds: kinds,
		newID: NewAssetID,
	}
}

// Store returns the underlying store, for the few callers that need the driver
// itself — the static route, which only a local store has.
func (b *Bundle) Store() Store {
	if b == nil {
		return nil
	}
	return b.store
}

// Name identifies the configured driver, for a startup log line.
func (b *Bundle) Name() string {
	if b == nil || b.store == nil {
		return "none"
	}
	return b.store.Name()
}

// Configured reports whether a store is available.
func (b *Bundle) Configured() bool { return b != nil && b.store != nil }

// KindsFor returns the rules for a kind.
func (b *Bundle) KindsFor(kind string) (Kinds, bool) {
	k, ok := b.kinds[kind]
	return k, ok
}

// AcceptedKinds returns the kind names, sorted.
//
// Exported so a caller can name the accepted set in its own error. A rejection
// that says only "unsupported kind" leaves the client guessing at a list the
// service already knows.
func (b *Bundle) AcceptedKinds() []string {
	names := make([]string, 0, len(b.kinds))
	for name := range b.kinds {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// URL returns where an object is readable.
func (b *Bundle) URL(objectKey string) string {
	if b == nil || b.store == nil {
		return ""
	}
	return b.store.URL(objectKey)
}

// Put validates an image and stores it, returning the reference that names it.
//
// # The order of the checks
//
// The type is validated before anything is written, and the type validated is
// the one sniffed from the bytes rather than the one the caller declared. That
// ordering is the point of the ceiling: a check made after the bytes have
// landed cannot un-land them, and a check made on a declaration checks the
// sender's opinion of their own file.
//
// # What it does not do
//
// It does not write the asset row. That is the caller's — the biz layer, which
// has the transaction and the identity — and keeping it out of here is what
// lets the store be swapped without the table's shape changing with it.
func (b *Bundle) Put(ctx context.Context, kind, ownerID, filename string, data []byte) (Ref, error) {
	if b == nil || b.store == nil {
		return Ref{}, ErrNotConfigured
	}

	rules, ok := b.kinds[kind]
	if !ok {
		return Ref{}, fmt.Errorf("%w: %q (accepts %s)", ErrUnsupportedKind, kind, strings.Join(b.AcceptedKinds(), ", "))
	}
	if len(data) == 0 {
		return Ref{}, fmt.Errorf("%w: nothing to store", ErrUnsupportedType)
	}

	// The declared type is taken from the filename and used only when sniffing
	// is inconclusive — see SniffImageType. It is not the caller's claim about
	// the bytes, because this signature has no such parameter: a type that is
	// never passed cannot be trusted by mistake.
	contentType := SniffImageType(data, mimeFromFilename(filename))
	if err := rules.Validate(contentType, int64(len(data))); err != nil {
		return Ref{}, err
	}

	if strings.TrimSpace(ownerID) == "" {
		ownerID = anonymousOwner
	}

	assetID := b.newID()
	name := sanitiseName(filename)
	if name == "file" {
		// Nothing usable was in the name, so the extension is what makes the
		// stored object readable in a listing.
		name = assetID + ExtensionFor(contentType)
	}

	objectKey, err := buildObjectKey(rules, ownerID, assetID, name)
	if err != nil {
		return Ref{}, err
	}

	obj, err := b.store.Put(ctx, PutRequest{
		ObjectKey:   objectKey,
		ContentType: contentType,
		Size:        int64(len(data)),
	}, bytes.NewReader(data))
	if err != nil {
		return Ref{}, err
	}

	width, height := ImageSize(data)
	return Ref{
		AssetID:   assetID,
		ObjectKey: objectKey,
		URL:       b.store.URL(objectKey),
		MimeType:  contentType,
		Size:      obj.Size,
		Width:     width,
		Height:    height,
		Name:      name,
		Source:    SourceUpload,
	}, nil
}

// Open returns an object's bytes, resolving the ref to a key first.
func (b *Bundle) Open(ctx context.Context, ref Ref) (io.ReadCloser, error) {
	key, err := b.keyFor(ref)
	if err != nil {
		return nil, err
	}
	return b.store.Open(ctx, key)
}

// OpenBytes reads an object whole, with its media type.
//
// The type is the ref's when it has one and the store's answer otherwise. A
// ref that came from a URL carries no type, and the caller about to base64 the
// bytes needs one — an octet-stream data URL is rejected by some model
// endpoints, so falling back to sniffing is more useful than an empty string.
func (b *Bundle) OpenBytes(ctx context.Context, ref Ref) ([]byte, string, error) {
	key, err := b.keyFor(ref)
	if err != nil {
		return nil, "", err
	}
	data, obj, err := OpenBytes(ctx, b.store, key)
	if err != nil {
		return nil, "", err
	}
	contentType := NormalizeContentType(ref.MimeType)
	if contentType == "" {
		contentType = NormalizeContentType(obj.MimeType)
	}
	if contentType == "" {
		contentType = SniffImageType(data, "")
	}
	return data, contentType, nil
}

// Stat reports an object's metadata.
func (b *Bundle) Stat(ctx context.Context, ref Ref) (Object, error) {
	key, err := b.keyFor(ref)
	if err != nil {
		return Object{}, err
	}
	return b.store.Stat(ctx, key)
}

// Delete removes an object. A missing object is not an error.
func (b *Bundle) Delete(ctx context.Context, ref Ref) error {
	key, err := b.keyFor(ref)
	if err != nil {
		return err
	}
	return b.store.Delete(ctx, key)
}

// keyFor resolves a ref to the object key this store addresses.
//
// # Why a bare URL is not enough
//
// A ref whose only field is a URL names an object this store did not receive,
// and there is no key to derive from it — the URL may be a remote host, or this
// store's own public path with a signature this process does not need. Callers
// holding such a ref fetch it; they do not Open it. Refusing here rather than
// guessing keeps that distinction visible.
func (b *Bundle) keyFor(ref Ref) (string, error) {
	if b == nil || b.store == nil {
		return "", ErrNotConfigured
	}
	if ref.ObjectKey != "" {
		return ref.ObjectKey, nil
	}
	if ref.AssetID != "" {
		return "", fmt.Errorf("media: asset %s has no object key; resolve it through the asset store", ref.AssetID)
	}
	return "", fmt.Errorf("%w: reference names no object in this store", ErrObjectNotFound)
}
