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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// PublicPath is the route objects are read from.
//
// It is a constant rather than a configuration value because the value ends up
// inside every stored Ref.URL: changing it would invalidate every URL already
// written to a session or a chat row, so it is the kind of setting that must
// not look configurable.
const PublicPath = "/media/"

// LocalStore keeps object bytes in a directory on this machine.
//
// # Why a directory is the default
//
// A single-node deployment has one machine, and a directory on it is a store
// that needs no bucket, no credentials, and no network. The interface above it
// is the same one an object store implements, so moving to COS or S3 later is
// a configuration change rather than a rewrite.
//
// # What it costs
//
// Bytes are readable only from the machine that wrote them. A second replica
// serves nothing, because its directory is empty. That is a property of the
// driver rather than a defect, and it is the reason to reach for its sibling:
// a deployment with more than one process sets media.driver to "cos", which
// puts the same bytes somewhere every replica can read. See COSStore.
type LocalStore struct {
	dir     string
	baseURL string
	signKey []byte
}

// NewLocalStore builds a directory-backed store.
//
// baseURL is the address a client reaches this service at, used to render
// absolute URLs. Empty is allowed and yields relative URLs, which is what a
// deployment behind a single origin wants.
func NewLocalStore(dir, baseURL string, signKey []byte) (*LocalStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("media: local store needs a directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("media: create object directory %s: %w", dir, err)
	}
	return &LocalStore{
		dir:     dir,
		baseURL: strings.TrimRight(baseURL, "/"),
		signKey: signKey,
	}, nil
}

// Name identifies the driver.
func (s *LocalStore) Name() string { return "local" }

// Dir returns the directory objects are written to, so a startup log can say
// where the files went.
func (s *LocalStore) Dir() string { return s.dir }

// Where implements Destination: a directory is named by its path.
func (s *LocalStore) Where() []any { return []any{"dir", s.dir} }

// Put writes an object, creating its directory.
//
// The bytes are written to a temporary file in the destination directory and
// renamed into place. A partial write is therefore never visible under the
// object's own key: anything that checks for the key's existence — a confirm
// step, a later Open — sees the object only once it is whole. Writing directly
// would leave a truncated file that looks like a successful upload to every
// one of them.
func (s *LocalStore) Put(_ context.Context, req PutRequest, r io.Reader) (Object, error) {
	full, err := s.resolve(req.ObjectKey)
	if err != nil {
		return Object{}, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return Object{}, fmt.Errorf("media: create directory for %s: %w", req.ObjectKey, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(full), ".upload-*")
	if err != nil {
		return Object{}, fmt.Errorf("media: create temp file for %s: %w", req.ObjectKey, err)
	}
	tmpName := tmp.Name()

	written, err := io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// The temporary file is removed: leaving it would accumulate one
		// invisible file per failed upload, and nothing ever sweeps for them.
		_ = os.Remove(tmpName)
		return Object{}, fmt.Errorf("media: write %s: %w", req.ObjectKey, err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		_ = os.Remove(tmpName)
		return Object{}, fmt.Errorf("media: commit %s: %w", req.ObjectKey, err)
	}

	return Object{ObjectKey: req.ObjectKey, Size: written, MimeType: req.ContentType}, nil
}

// Open returns the object's bytes.
func (s *LocalStore) Open(_ context.Context, objectKey string) (io.ReadCloser, error) {
	full, err := s.resolve(objectKey)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrObjectNotFound, objectKey)
	}
	if err != nil {
		return nil, fmt.Errorf("media: open %s: %w", objectKey, err)
	}
	return f, nil
}

// Stat reports the object's size, or ErrObjectNotFound.
//
// The media type is not reported: this store does not record one beside the
// bytes, and inventing one by sniffing here would make Stat cost a read. The
// caller that needs a type has the value it sniffed at upload time, in the
// asset row or in the Ref.
func (s *LocalStore) Stat(_ context.Context, objectKey string) (Object, error) {
	full, err := s.resolve(objectKey)
	if err != nil {
		return Object{}, err
	}
	info, err := os.Stat(full)
	if errors.Is(err, os.ErrNotExist) {
		return Object{}, fmt.Errorf("%w: %s", ErrObjectNotFound, objectKey)
	}
	if err != nil {
		return Object{}, fmt.Errorf("media: stat %s: %w", objectKey, err)
	}
	return Object{ObjectKey: objectKey, Size: info.Size()}, nil
}

// Delete removes an object. A missing object is not an error.
func (s *LocalStore) Delete(_ context.Context, objectKey string) error {
	if objectKey == "" {
		return nil
	}
	full, err := s.resolve(objectKey)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("media: delete %s: %w", objectKey, err)
	}
	// The asset's own directory is removed, and then the owner's above it when
	// this was that owner's last object. Both removals fail harmlessly when the
	// directory is not empty — somebody else's asset is still in the owner one —
	// so neither needs a check. Leaving them is what turns a store into a
	// listing of things that no longer exist.
	_ = os.Remove(filepath.Dir(full))
	_ = os.Remove(filepath.Dir(filepath.Dir(full)))
	return nil
}

// URL returns where the object is readable.
func (s *LocalStore) URL(objectKey string) string {
	return s.baseURL + PublicPath + objectKey + s.signature(objectKey)
}

// signature returns the query suffix that proves a path was handed out by this
// service, or "" when no signing key is configured.
//
// # What it does and does not buy
//
// It is an HMAC of the key alone, so it is stable: the same key always yields
// the same URL, which is what lets a URL be stored in a Ref. The benefit is
// that guessing a key is not enough to read it — the secret is needed too,
// which matters for a store whose keys are derived from anything enumerable.
//
// It deliberately does **not** expire, and the reason is the storage. An
// expiring signature cannot be written to a session or a chat row, because it
// would be dead by the time it was read back; making it expire would mean
// signing at render time, at every surface that emits a URL. So this is an
// unguessability measure, not an access-control one — the object is public to
// anyone the URL reaches.
func (s *LocalStore) signature(objectKey string) string {
	if len(s.signKey) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, s.signKey)
	mac.Write([]byte(objectKey))
	return "?s=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether a signature presented for a key is one this store
// would have issued.
func (s *LocalStore) Verify(objectKey, sig string) bool {
	if len(s.signKey) == 0 {
		// Nothing is signed, so nothing is verified. Accepting anything here
		// is the same answer as the URL having no signature at all.
		return true
	}
	want := strings.TrimPrefix(s.signature(objectKey), "?s=")
	return hmac.Equal([]byte(want), []byte(sig))
}

// ServeHTTP serves a stored object.
//
// It is an http.Handler rather than a framework-specific one so this package
// stays free of the router: the service wraps it, and the wrapping is where
// the route's authentication belongs.
func (s *LocalStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	key := strings.TrimPrefix(r.URL.Path, PublicPath)
	// Whitespace and ".." are refused rather than cleaned: the key came from a
	// URL, so cleaning it would let a crafted path read outside the directory.
	if key == "" || strings.Contains(key, "..") || !isSafeKey(key) {
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}
	if !s.Verify(key, r.URL.Query().Get("s")) {
		http.Error(w, "bad signature", http.StatusForbidden)
		return
	}

	full, err := s.resolve(key)
	if err != nil {
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}
	// http.ServeFile answers 404 for a missing file and handles Range, HEAD and
	// conditional requests, none of which are worth reimplementing.
	http.ServeFile(w, r, full)
}

// resolve turns an object key into an absolute path inside the store's
// directory, refusing anything that would land outside it.
//
// The check is repeated here rather than trusted from the caller. Delete and
// Stat are reached from the service rather than from a URL, so their keys are
// not attacker-controlled the way an upload's is — but a key that escaped would
// let a delete reach any file the process can write, which is a worse outcome
// than an upload landing in the wrong place.
func (s *LocalStore) resolve(objectKey string) (string, error) {
	if objectKey == "" || path.IsAbs(objectKey) || strings.Contains(objectKey, "..") || !isSafeKey(objectKey) {
		return "", fmt.Errorf("%w: %q", ErrInvalidObjectKey, objectKey)
	}
	abs, err := filepath.Abs(filepath.Join(s.dir, filepath.FromSlash(objectKey)))
	if err != nil {
		return "", fmt.Errorf("media: resolve %s: %w", objectKey, err)
	}
	root, err := filepath.Abs(s.dir)
	if err != nil {
		return "", fmt.Errorf("media: resolve root: %w", err)
	}
	if !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q escapes the object directory", ErrInvalidObjectKey, objectKey)
	}
	return abs, nil
}

// isSafeKey reports whether every segment of an object key is one this store is
// willing to create on disk.
func isSafeKey(key string) bool {
	if path.IsAbs(key) {
		return false
	}
	for segment := range strings.SplitSeq(key, "/") {
		if !isSafeSegment(segment) {
			return false
		}
	}
	return true
}
