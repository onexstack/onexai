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
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tencentyun/cos-go-sdk-v5"
)

// COSStore keeps object bytes in a Tencent COS bucket.
//
// # Why a bucket
//
// LocalStore writes to a directory on the machine that received the upload, so
// a second replica serves nothing: its directory is empty. A bucket is the same
// interface with the bytes somewhere every replica can reach, which is what a
// deployment with more than one process needs.
//
// # Why a prefix
//
// One bucket often holds more than one service's objects, so prefix namespaces
// the keys this store writes. The namespace is added here rather than in the
// key the bundle builds, so a stored Ref keeps naming the object the same way
// it did under LocalStore -- the deployment can move the objects into a
// namespace without rewriting a single row.
//
// # What it costs
//
// The URL this store hands out is the object's address, not a grant: a bucket
// that is not publicly readable answers 403 to it. That is deliberate. A signed
// URL carries an expiry, and an expiry cannot be written into the asset row and
// the session that reference it -- the row would outlive its own link. A
// deployment that needs a client to render an object gives the bucket public
// read, or fronts it with a CDN, or reads the bytes back through the service's
// authenticated content route, which goes through Open and works either way.
type COSStore struct {
	client *cos.Client
	// base is the address object URLs are built from: the bucket's own domain,
	// or the configured override (a CDN, an accelerated domain). No trailing
	// slash, so a URL is base + "/" + key.
	base string
	// bucket is the host, kept for the startup log: "which bucket" is the fact
	// an operator gets wrong for this driver.
	bucket string
	// prefix is the key namespace inside the bucket, no leading or trailing
	// slash. Empty writes at the bucket root.
	prefix string
}

var _ Store = (*COSStore)(nil)

// NewCOSStore builds a bucket-backed store.
//
// bucketURL is the bucket endpoint -- "name-appid.cos.region.myqcloud.com" --
// with or without a scheme; https is assumed when it is omitted. publicBaseURL
// overrides the domain object URLs are built from and is empty for the bucket's
// own. prefix is the key namespace inside the bucket and is empty for the root.
func NewCOSStore(bucketURL, secretID, secretKey, publicBaseURL, prefix string) (*COSStore, error) {
	u, err := parseBucketURL(bucketURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(secretID) == "" || strings.TrimSpace(secretKey) == "" {
		return nil, errors.New("media: cos store needs a secret id and secret key")
	}
	ns, err := normalisePrefix(prefix)
	if err != nil {
		return nil, err
	}

	// The SDK's own Check is a shape test on the URL we just parsed; asking it
	// is cheaper than reimplementing and it is the same test the client will
	// apply to every request, so a URL that fails here would have failed later
	// with a less specific error.
	b := &cos.BaseURL{BucketURL: u}
	if !b.Check() {
		return nil, fmt.Errorf("media: cos bucket url %q is not a cos endpoint", bucketURL)
	}

	base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	if base == "" {
		base = u.Scheme + "://" + u.Host
	}
	return &COSStore{
		client: cos.NewClient(b, &http.Client{
			// AuthorizationTransport signs each request and is safe for
			// concurrent use; the client it is attached to is shared by every
			// request this store makes, so neither may be mutated afterwards.
			Transport: &cos.AuthorizationTransport{SecretID: secretID, SecretKey: secretKey},
		}),
		base:   base,
		bucket: u.Hostname(),
		prefix: ns,
	}, nil
}

// parseBucketURL normalises a bucket endpoint into the URL the SDK needs.
//
// The scheme is not optional to the SDK: BaseURL.Check fails without one and
// every request would come back as an invalid-URL error, so it is supplied here
// rather than left for an operator to discover.
func parseBucketURL(bucketURL string) (*url.URL, error) {
	raw := strings.TrimSpace(bucketURL)
	if raw == "" {
		return nil, errors.New("media: cos store needs a bucket url")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("media: parse cos bucket url %q: %w", bucketURL, err)
	}
	// A host with no dot is a typo far more often than a real endpoint (an
	// internal resolver would be the exception), and the failure it produces
	// later is a DNS error that says nothing about the config.
	if !strings.Contains(u.Hostname(), ".") {
		return nil, fmt.Errorf("media: cos bucket url %q names no host", bucketURL)
	}
	// The bucket root is the only base that means anything here: the SDK
	// resolves an absolute key against it and would drop a path silently, so a
	// path is refused rather than ignored.
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("media: cos bucket url %q must be scheme://host", bucketURL)
	}
	return u, nil
}

// normalisePrefix trims the slashes an operator naturally types around a path
// and refuses one that is not a path this store would write.
func normalisePrefix(prefix string) (string, error) {
	ns := strings.Trim(strings.TrimSpace(prefix), "/")
	if ns == "" {
		return "", nil
	}
	// The same rule the keys obey, because the prefix becomes part of every
	// key: a prefix that could escape the bucket root, or that carried a
	// character the key builder would have refused, is a configuration error
	// rather than something to sanitise into a different namespace than the
	// one that was asked for.
	if !isSafeKey(ns) {
		return "", fmt.Errorf("%w: cos prefix %q", ErrInvalidObjectKey, prefix)
	}
	return ns, nil
}

// Name identifies the driver.
func (s *COSStore) Name() string { return "cos" }

// Bucket returns the bucket host, for a startup log line.
func (s *COSStore) Bucket() string { return s.bucket }

// Prefix returns the key namespace inside the bucket, for a startup log line.
func (s *COSStore) Prefix() string { return s.prefix }

// Where implements Destination: a bucket is named by its host and the prefix
// the keys sit under. Both halves matter — an object one level away from where
// the operator looked is this driver's characteristic failure.
func (s *COSStore) Where() []any {
	return []any{"bucket", s.bucket, "prefix", s.prefix}
}

// bucketKey maps the logical key the bundle builds to the address inside the
// bucket. Every method goes through it, so the namespace is a property of the
// deployment and not of any stored reference.
func (s *COSStore) bucketKey(objectKey string) string {
	if s.prefix == "" {
		return objectKey
	}
	return s.prefix + "/" + objectKey
}

// checkKey refuses a key this store is unwilling to address.
//
// The bundle only ever builds safe keys, but Open, Stat and Delete are reached
// from a stored Ref, and a Ref's key is as trustworthy as whatever wrote the
// row. Refusing here keeps this driver and LocalStore agreeing about what a key
// is, which is what lets the same row be read under either one.
func checkKey(objectKey string) (string, error) {
	if !isSafeKey(objectKey) {
		return "", fmt.Errorf("%w: %q", ErrInvalidObjectKey, objectKey)
	}
	return objectKey, nil
}

// readKey is checkKey plus the empty-key rule. Nothing lives at an empty key,
// so reading one is a missing object rather than a malformed key -- the two are
// answered differently by callers, and only one of them is a bug.
func readKey(objectKey string) (string, error) {
	if strings.TrimSpace(objectKey) == "" {
		return "", fmt.Errorf("%w: empty object key", ErrObjectNotFound)
	}
	return checkKey(objectKey)
}

// Put writes an object.
func (s *COSStore) Put(ctx context.Context, req PutRequest, r io.Reader) (Object, error) {
	key, err := checkKey(req.ObjectKey)
	if err != nil {
		return Object{}, err
	}
	if r == nil {
		return Object{}, fmt.Errorf("media: cos put %s: nothing to write", key)
	}

	// The length is stated rather than left to the SDK. Given it, the transport
	// sends a sized body and a caller whose bytes do not match what it
	// announced gets an error, instead of an object that is quietly short.
	opt := &cos.ObjectPutOptions{
		ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{
			ContentType:   req.ContentType,
			ContentLength: req.Size,
		},
	}
	if _, err := s.client.Object.Put(ctx, s.bucketKey(key), r, opt); err != nil {
		return Object{}, fmt.Errorf("media: cos put %s: %w", key, err)
	}
	// The upload carries no size back, but the transport enforced the one that
	// was sent, so announcing the same value is reporting rather than assuming.
	return Object{ObjectKey: key, Size: req.Size, MimeType: req.ContentType}, nil
}

// Open returns the object's bytes.
func (s *COSStore) Open(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	key, err := readKey(objectKey)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Object.Get(ctx, s.bucketKey(key), nil)
	if err != nil {
		// A missing object is an answer, not a failure: callers branch on it to
		// tell "expired and swept" from "the bucket is unreachable".
		if cos.IsNotFoundError(err) {
			return nil, fmt.Errorf("%w: %s", ErrObjectNotFound, key)
		}
		return nil, fmt.Errorf("media: cos open %s: %w", key, err)
	}
	// The SDK leaves a successful body open for the caller; the callers of Open
	// close it, which is why this returns it rather than reading it.
	return resp.Body, nil
}

// Stat reports the object's metadata.
func (s *COSStore) Stat(ctx context.Context, objectKey string) (Object, error) {
	key, err := readKey(objectKey)
	if err != nil {
		return Object{}, err
	}
	resp, err := s.client.Object.Head(ctx, s.bucketKey(key), nil)
	if err != nil {
		if cos.IsNotFoundError(err) {
			return Object{}, fmt.Errorf("%w: %s", ErrObjectNotFound, key)
		}
		return Object{}, fmt.Errorf("media: cos stat %s: %w", key, err)
	}

	// The size has to be the real one: OpenBytes reads through a limit of it,
	// so a size that is too small truncates the object and a size that is too
	// large lets a changed object be read past its end. Better to fail than to
	// invent a number here.
	size := resp.ContentLength
	if size < 0 {
		if n, perr := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); perr == nil {
			size = n
		}
	}
	if size < 0 {
		return Object{}, fmt.Errorf("media: cos stat %s: response carried no content length", key)
	}

	// Unlike LocalStore, the media type is free here, so it is reported. Bundle
	// prefers a type the ref already carries and only falls back to this, so
	// answering is strictly more than the driver is asked for.
	return Object{
		ObjectKey: key,
		Size:      size,
		MimeType:  NormalizeContentType(resp.Header.Get("Content-Type")),
	}, nil
}

// Delete removes an object. A missing object is not an error.
func (s *COSStore) Delete(ctx context.Context, objectKey string) error {
	// An empty key names nothing, so there is nothing to delete and nothing to
	// report. The SDK would refuse it as a malformed name, which would make a
	// no-op look like a failure.
	if strings.TrimSpace(objectKey) == "" {
		return nil
	}
	key, err := checkKey(objectKey)
	if err != nil {
		return err
	}
	if _, err := s.client.Object.Delete(ctx, s.bucketKey(key)); err != nil {
		// Deleting a key that is not there is the state the caller asked for.
		if cos.IsNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("media: cos delete %s: %w", key, err)
	}
	return nil
}

// URL returns where the object is addressed.
//
// It never fails and never expires, because it is written into the asset row
// and into every reference that names the object: a URL that needed a request,
// or that went stale, would make a stored row unreadable through no fault of
// its own. See the type comment for what a non-public bucket means for it.
func (s *COSStore) URL(objectKey string) string {
	// No object, no address. Answering with the bucket root would look like a
	// link to something.
	if objectKey == "" {
		return ""
	}
	// The key is built from safe segments and the prefix is held to the same
	// rule, so no escaping is needed. The base is never empty: the constructor
	// falls back to the bucket's own domain.
	return s.base + "/" + s.bucketKey(objectKey)
}
