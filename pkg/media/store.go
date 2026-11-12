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
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Sentinel errors.
var (
	// ErrNotConfigured means no store is available. It is distinct from a
	// provider failure: "there is nowhere bytes could have gone" is a
	// deployment answer, and reporting it as a timeout sends someone to look
	// at a subsystem that is working.
	ErrNotConfigured = errors.New("media: no store is configured")
	// ErrUnsupportedKind means the requested kind is not one this deployment
	// accepts.
	ErrUnsupportedKind = errors.New("media: unsupported asset kind")
	// ErrUnsupportedType means the object's media type is not accepted.
	ErrUnsupportedType = errors.New("media: unsupported content type")
	// ErrTooLarge means the object exceeds the kind's ceiling.
	ErrTooLarge = errors.New("media: object is too large")
	// ErrObjectNotFound means the object does not exist in the store.
	ErrObjectNotFound = errors.New("media: object not found")
	// ErrInvalidObjectKey means a key could not be assembled from its parts.
	ErrInvalidObjectKey = errors.New("media: unusable object key segment")
	// ErrUnsupportedDriver means an unrecognised driver was configured.
	ErrUnsupportedDriver = errors.New("media: unsupported driver")
)

// Asset kinds. The kind decides the size ceiling, the accepted types, and the
// key prefix.
const (
	// KindImage is a user-supplied image: what a person sent, or what was
	// downloaded from the IM platform on their behalf.
	KindImage = "image"
	// KindGenerated is an image a model produced. It gets a larger ceiling
	// than a photograph because generation endpoints return large PNGs that
	// no upstream has compressed.
	KindGenerated = "generated"
	// KindAvatar is an image shown beside a person's name. It is deliberately
	// the tightest: an avatar is fetched on every surface that renders the
	// person, so its size is multiplied by traffic.
	KindAvatar = "avatar"
)

// DefaultKinds are the limits this service enforces.
//
// The accepted types come from imageFormats, so a format added there is
// accepted here without a second edit — and one removed cannot linger in a
// list that would then accept a type the filename mapping no longer knows.
func DefaultKinds() map[string]Kinds {
	return map[string]Kinds{
		KindImage: {
			MaxBytes:     5 << 20, // 5 MiB
			ContentTypes: imageMimes(),
			Prefix:       "images",
		},
		KindGenerated: {
			MaxBytes:     10 << 20, // 10 MiB
			ContentTypes: imageMimes("image/gif"),
			Prefix:       "generated",
		},
		KindAvatar: {
			MaxBytes:     2 << 20, // 2 MiB
			ContentTypes: imageMimes("image/gif"),
			Prefix:       "avatars",
		},
	}
}

// Kinds describes what one asset kind accepts.
type Kinds struct {
	// MaxBytes is the ceiling for the kind.
	MaxBytes int64
	// ContentTypes are the accepted MIME types.
	ContentTypes []string
	// Prefix is the key prefix, so the store can be browsed by kind and a
	// per-prefix lifecycle rule stays possible.
	Prefix string
}

// Validate checks a content type and size against the kind's rules.
//
// The type checked here is the one sniffed from the bytes, never the client's
// declaration. A declared type is a claim about a file the sender controls,
// and the whole point of the ceiling is that the sender cannot be trusted about
// it. See SniffImageType.
func (k Kinds) Validate(contentType string, size int64) error {
	if size < 0 {
		return fmt.Errorf("media: negative size %d", size)
	}
	if size > k.MaxBytes {
		return fmt.Errorf("%w: %d bytes exceeds the %d allowed", ErrTooLarge, size, k.MaxBytes)
	}
	ct := NormalizeContentType(contentType)
	if ct == "" {
		return fmt.Errorf("%w: nothing said what this file is", ErrUnsupportedType)
	}
	if slices.Contains(k.ContentTypes, ct) {
		return nil
	}
	return fmt.Errorf("%w: %q (accepts %s)", ErrUnsupportedType, ct, strings.Join(k.ContentTypes, ", "))
}

// NormalizeContentType lower-cases a media type and drops its parameters.
//
// "image/JPEG; charset=binary" is the same type as "image/jpeg", and a client
// that sends the long form must not be rejected for it. Not doing this is a
// common source of "it works from curl but not from the browser".
func NormalizeContentType(ct string) string {
	ct = strings.TrimSpace(strings.ToLower(ct))
	if semi := strings.IndexByte(ct, ';'); semi >= 0 {
		ct = strings.TrimSpace(ct[:semi])
	}
	return ct
}

// safeSegmentRE matches a key segment that cannot escape its prefix.
//
// The character class alone is not enough: "." and ".." are made of allowed
// characters and are exactly the segments that escape. They are rejected
// separately, by isSafeSegment, because path.Join collapses them — a check that
// only looked at characters would accept ".." and then silently write outside
// the prefix.
var safeSegmentRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// isSafeSegment reports whether a single path segment may be used in a key.
func isSafeSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	return safeSegmentRE.MatchString(segment)
}

// unsafeNameRunesRE matches a run of characters that may not appear in a key's
// filename segment.
var unsafeNameRunesRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// maxNameBytes bounds the filename segment of a key.
//
// The segments ahead of the name are bounded too (a short prefix, a bounded
// owner id, a 32-char asset id), so a name kept under this keeps the whole key
// inside a 512-byte column. Without a bound, a long name fails at the last step
// for a detail nothing depends on.
const maxNameBytes = 96

// sanitiseName turns a client-supplied filename into a path segment.
//
// # Why it replaces instead of refusing
//
// The filename is the one part of a key a client chooses, and the names a
// browser sends are ordinary rather than tidy: "IMG_0001 (1).jpg" from a phone,
// "屏幕截图 2026-10-03 003144.png" from a screenshot tool. Refusing every name
// outside [A-Za-z0-9._-] turns the commonest screenshots into 400s, and the
// request was one the service should serve.
//
// Nothing is lost by replacing them. A key's identity is the asset id in the
// directory above the name — that is what keeps two uploads distinct — and the
// name is decoration that makes the store browsable. A mangled name costs a
// reader of the store a little clarity and costs a client nothing.
//
// The traversal protections do not depend on the characters kept here: the
// segment is taken from path.Base, it is a single segment by construction, and
// it can never be "." or "..".
func sanitiseName(filename string) string {
	// Base first: a client that sends a full path ("/tmp/photo.png") does
	// not get to choose the directory the object lands in.
	name := path.Base(strings.TrimSpace(filename))
	// path.Base answers "." for "", "." and ".." — the inputs that name
	// nothing, or would name the parent.
	if name == "." || name == ".." {
		name = ""
	}
	// A leading dot is dropped so ".png" becomes "png": a hidden file's name
	// carries nothing a key needs, and the alternative is an empty basename.
	name = strings.TrimLeft(name, ".")
	// Everything outside the safe set becomes an underscore, runs collapsed,
	// so "my photo.png" reads as "my_photo.png" rather than "my__photo.png".
	name = unsafeNameRunesRE.ReplaceAllString(name, "_")
	if name == "" {
		name = "file"
	}
	// Cutting by bytes is safe because everything above is ASCII: every
	// multi-byte rune was already replaced with "_".
	if len(name) > maxNameBytes {
		name = name[:maxNameBytes]
	}
	return name
}

// BuildObjectKey composes a key under the kind's prefix.
//
// The asset id is a path segment of its own, and that is not decoration. Every
// object gets a directory of its own, so a user who uploads two files both
// called IMG_0001.jpg gets two keys rather than one written twice — where the
// second upload would silently replace the first and the first row would go on
// pointing at a URL that now serves someone else's photo.
//
// The owner and asset ids are checked rather than sanitised, while the name is
// sanitised rather than trusted. The asymmetry is deliberate: the name is
// something a client chose from a phone's file list and is made workable, while
// this process's own ids failing the check is a bug with no sensible repair.
func BuildObjectKey(kind Kinds, ownerID, assetID, filename string) (string, error) {
	return buildObjectKey(kind, ownerID, assetID, sanitiseName(filename))
}

// buildObjectKey composes the key from a filename segment the caller has already
// sanitised.
//
// It is the half of BuildObjectKey that does not sanitise, so a caller that
// needed the sanitised name for its own decision — Bundle.Put, which replaces an
// unusable name with the asset id plus an extension — does not sanitise the same
// string twice. The exported wrapper still sanitises, because a key builder that
// trusted an arbitrary filename would be the one place traversal protection is
// worth having.
func buildObjectKey(kind Kinds, ownerID, assetID, safeName string) (string, error) {
	if !isSafeSegment(ownerID) {
		return "", fmt.Errorf("%w: owner id %q", ErrInvalidObjectKey, ownerID)
	}
	if !isSafeSegment(assetID) {
		return "", fmt.Errorf("%w: asset id %q", ErrInvalidObjectKey, assetID)
	}
	if !isSafeSegment(safeName) {
		return "", fmt.Errorf("%w: filename segment %q", ErrInvalidObjectKey, safeName)
	}
	// The owner directory keeps one user's objects together, which is what
	// makes a per-user quota and a per-prefix lifecycle rule possible.
	return path.Join(kind.Prefix, ownerID, assetID, safeName), nil
}

// Object is what a store knows about a stored object.
type Object struct {
	// ObjectKey is the store-relative key.
	ObjectKey string
	// Size is the object size in bytes.
	Size int64
	// MimeType is the media type, as sniffed when the object was written.
	MimeType string
}

// PutRequest names the object to write.
type PutRequest struct {
	// ObjectKey is the store-relative key. The caller builds it, because the
	// caller has the asset id the key is built from — generating the key in
	// two places is how the store and the asset row come to disagree.
	ObjectKey string
	// ContentType is the sniffed media type.
	ContentType string
	// Size is the object size in bytes.
	Size int64
}

// Store stores and serves object bytes.
//
// It is the seam over the medium: a directory on disk, or an object store.
// Nothing above it knows which, and the one thing that does — NewStore — is
// where the choice is made.
type Store interface {
	// Put writes an object. The reader is consumed once.
	Put(ctx context.Context, req PutRequest, r io.Reader) (Object, error)
	// Open returns the object's bytes.
	Open(ctx context.Context, objectKey string) (io.ReadCloser, error)
	// Stat reports the object's metadata, or ErrObjectNotFound.
	Stat(ctx context.Context, objectKey string) (Object, error)
	// Delete removes an object. A missing object is not an error.
	Delete(ctx context.Context, objectKey string) error
	// URL returns the object's address on this store, absolute when the
	// deployment configured a public base URL.
	//
	// # It is an address, not a grant
	//
	// Nothing here promises the address can be fetched. A directory this process
	// serves answers; an object store's private bucket answers 403, and that is
	// a decision rather than an oversight: a signed link carries an expiry, and
	// an expiry cannot be written into an asset row or a session without the row
	// outliving its own URL. Every driver therefore returns the plain address,
	// and a caller that needs the bytes reads them back through this service —
	// see the asset content route — rather than following the URL itself.
	URL(objectKey string) string
	// Name identifies the driver, for a startup log line.
	Name() string
}

// OpenBytes reads an object whole.
//
// It is a convenience for the callers that genuinely need the bytes in memory —
// the model edge, which is about to base64 them — and it is deliberately named
// so that a caller doing anything else sees that they are not streaming.
func OpenBytes(ctx context.Context, s Store, objectKey string) ([]byte, Object, error) {
	rc, err := s.Open(ctx, objectKey)
	if err != nil {
		return nil, Object{}, err
	}
	defer rc.Close()
	obj, err := s.Stat(ctx, objectKey)
	if err != nil {
		return nil, Object{}, err
	}
	// Read one byte past the declared size so a store that lied about it is
	// caught rather than trusted: the bytes are about to be held in memory and
	// base64-expanded, so an unbounded read is a memory bound the process does
	// not have.
	data, err := io.ReadAll(io.LimitReader(rc, obj.Size+1))
	if err != nil {
		return nil, Object{}, fmt.Errorf("media: read %s: %w", objectKey, err)
	}
	if int64(len(data)) > obj.Size {
		return nil, Object{}, fmt.Errorf("media: object %s is larger than the %d bytes recorded for it", objectKey, obj.Size)
	}
	return data, obj, nil
}
