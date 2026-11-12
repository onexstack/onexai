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
	"os"
	"strings"
	"testing"
)

const (
	cosTestBucket = "onexai-test-1254073058.cos.ap-guangzhou.myqcloud.com"

	// cosTestEnvBucket is the switch for the round-trip test. Its absence is the
	// only thing that keeps the test from running, because a bucket is a real
	// dependency and a unit test that needs one is a test that fails on a
	// machine with no network rather than a test that found something.
	cosTestEnvBucket = "ONEXAI_COS_TEST_BUCKET_URL"
	cosTestEnvID     = "ONEXAI_COS_TEST_SECRET_ID"
	cosTestEnvKey    = "ONEXAI_COS_TEST_SECRET_KEY"
	cosTestEnvPrefix = "ONEXAI_COS_TEST_PREFIX"

	// cosTestMissingKey is a well-formed key nothing has ever written to. Its
	// shape matters: a malformed one would be refused by the driver's own key
	// check and would prove nothing about the bucket's answer.
	cosTestMissingKey = "images/ou_nobody/img_00000000000000000000000000000000/missing.png"
)

// TestNewCOSStoreNormalisesBucketURL pins that a bucket url an operator would
// write by hand is accepted. The scheme is not decoration: the SDK refuses a
// url without one on every request, so supplying it here is what keeps a
// scheme-less config from looking like a network problem.
func TestNewCOSStoreNormalisesBucketURL(t *testing.T) {
	s, err := NewCOSStore(cosTestBucket, "an-id", "a-key", "", "")
	if err != nil {
		t.Fatalf("NewCOSStore: %v", err)
	}
	if s.Name() != "cos" {
		t.Errorf("Name() = %q, want cos", s.Name())
	}
	if s.Bucket() != cosTestBucket {
		t.Errorf("Bucket() = %q, want %q", s.Bucket(), cosTestBucket)
	}
	if s.Prefix() != "" {
		t.Errorf("Prefix() = %q, want empty", s.Prefix())
	}

	const key = "images/ou_abc/img_1/a.png"
	want := "https://" + cosTestBucket + "/" + key
	if got := s.URL(key); got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}

	// A scheme that is already there is left alone, so an http deployment is
	// not silently upgraded to https.
	withScheme, err := NewCOSStore("http://"+cosTestBucket, "an-id", "a-key", "", "")
	if err != nil {
		t.Fatalf("NewCOSStore with a scheme: %v", err)
	}
	if got := withScheme.URL(key); !strings.HasPrefix(got, "http://") {
		t.Errorf("URL() = %q, want it to keep the http scheme it was given", got)
	}
}

func TestNewCOSStoreRejectsBadInput(t *testing.T) {
	tests := []struct {
		name      string
		bucket    string
		secretID  string
		secretKey string
		prefix    string
	}{
		{name: "no bucket", bucket: "", secretID: "an-id", secretKey: "a-key"},
		{
			// A host with no dot is a typo far more often than an endpoint, and
			// the DNS error it produces later names nothing an operator can fix.
			name: "a host with no dot", bucket: "mybucket", secretID: "an-id", secretKey: "a-key",
		},
		{
			name: "a bucket url carrying a path", bucket: cosTestBucket + "/prefix",
			secretID: "an-id", secretKey: "a-key",
		},
		{
			name: "a bucket url carrying a query", bucket: cosTestBucket + "?x=1",
			secretID: "an-id", secretKey: "a-key",
		},
		{name: "no secret id", bucket: cosTestBucket, secretKey: "a-key"},
		{name: "no secret key", bucket: cosTestBucket, secretID: "an-id"},
		{
			// The prefix becomes part of every key, so one that could climb out
			// of the namespace is refused rather than trimmed into a different
			// namespace than the one that was asked for. A *leading* slash is
			// not that: it is trimmed, and TestCOSStorePrefixIsPartOfTheAddress
			// pins it, because a prefix is always relative to the bucket root.
			name: "a prefix that climbs out", bucket: cosTestBucket,
			secretID: "an-id", secretKey: "a-key", prefix: "a/../../b",
		},
		{
			name: "a prefix with an empty segment", bucket: cosTestBucket,
			secretID: "an-id", secretKey: "a-key", prefix: "a//b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewCOSStore(tt.bucket, tt.secretID, tt.secretKey, "", tt.prefix); err == nil {
				t.Fatal("NewCOSStore() = nil error, want a refusal")
			}
		})
	}
}

// TestCOSStorePrefixIsPartOfTheAddress pins that the namespace reaches the
// bucket and the URL, and that the slashes an operator types around a path are
// not part of it.
func TestCOSStorePrefixIsPartOfTheAddress(t *testing.T) {
	const key = "images/ou_abc/img_1/a.png"

	for _, prefix := range []string{"agent/images", "/agent/images", "agent/images/", "/agent/images/"} {
		s, err := NewCOSStore(cosTestBucket, "an-id", "a-key", "", prefix)
		if err != nil {
			t.Fatalf("NewCOSStore(prefix=%q): %v", prefix, err)
		}
		if s.Prefix() != "agent/images" {
			t.Errorf("Prefix() for input %q = %q, want agent/images", prefix, s.Prefix())
		}
		want := "https://" + cosTestBucket + "/agent/images/" + key
		if got := s.URL(key); got != want {
			t.Errorf("URL() for input %q = %q, want %q", prefix, got, want)
		}
	}

	// An empty prefix is the bucket root rather than a namespace named "".
	root, err := NewCOSStore(cosTestBucket, "an-id", "a-key", "", "")
	if err != nil {
		t.Fatalf("NewCOSStore: %v", err)
	}
	if got, want := root.URL(key), "https://"+cosTestBucket+"/"+key; got != want {
		t.Errorf("URL() at the bucket root = %q, want %q", got, want)
	}
	// A prefix of only slashes is also the root: it names no namespace.
	slashes, err := NewCOSStore(cosTestBucket, "an-id", "a-key", "", "///")
	if err != nil {
		t.Fatalf("NewCOSStore(prefix=\"///\"): %v", err)
	}
	if got, want := slashes.URL(key), "https://"+cosTestBucket+"/"+key; got != want {
		t.Errorf("URL() for a slash-only prefix = %q, want %q", got, want)
	}
}

// TestCOSStoreURLIsStableAndUnsigned pins the property the whole design rests
// on: the URL is written into the asset row and into every reference that names
// the object, so it cannot be something that expires or that needs a request to
// produce. The local driver's signature is the counterpart of this; a bucket's
// presigned URL is exactly what must not appear here.
func TestCOSStoreURLIsStableAndUnsigned(t *testing.T) {
	const key = "images/ou_abc/img_1/a.png"

	s, err := NewCOSStore(cosTestBucket, "an-id", "a-key", "", "agent")
	if err != nil {
		t.Fatalf("NewCOSStore: %v", err)
	}
	first := s.URL(key)
	if second := s.URL(key); second != first {
		t.Errorf("URL() is not stable: %q then %q", first, second)
	}
	if strings.Contains(first, "?") {
		t.Errorf("URL() = %q, want no query string: a presigned url expires and cannot be stored", first)
	}

	// The public base overrides the domain and its trailing slash is not part
	// of the address.
	cdn, err := NewCOSStore(cosTestBucket, "an-id", "a-key", "https://cdn.example.com/", "")
	if err != nil {
		t.Fatalf("NewCOSStore with a public base: %v", err)
	}
	if got, want := cdn.URL(key), "https://cdn.example.com/"+key; got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}

	// No object, no address. Answering with the bucket root would look like a
	// link to something.
	if got := s.URL(""); got != "" {
		t.Errorf("URL(\"\") = %q, want empty", got)
	}
}

// TestCOSStoreRejectsUnsafeKeys pins the key contract without a bucket.
//
// The checks run before any request is built, which is what makes this test
// worth having: a key that came out of a stored reference is refused for the
// same reason LocalStore refuses it, and the two drivers agree about what a
// key is without either one making a network call to find out.
func TestCOSStoreRejectsUnsafeKeys(t *testing.T) {
	s, err := NewCOSStore(cosTestBucket, "an-id", "a-key", "", "agent")
	if err != nil {
		t.Fatalf("NewCOSStore: %v", err)
	}
	ctx := context.Background()

	unsafe := []string{"../secret.png", "images/../../secret.png", "/etc/passwd", "a//b"}

	// Nothing lives at an empty key, so a read of one is a missing object
	// rather than a malformed key. The two answers lead somewhere different.
	if _, err := s.Open(ctx, ""); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Open(\"\") = %v, want ErrObjectNotFound", err)
	}
	if _, err := s.Stat(ctx, ""); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Stat(\"\") = %v, want ErrObjectNotFound", err)
	}
	if err := s.Delete(ctx, ""); err != nil {
		t.Errorf("Delete(\"\") = %v, want nil", err)
	}

	for _, key := range unsafe {
		t.Run(key, func(t *testing.T) {
			if _, err := s.Open(ctx, key); !errors.Is(err, ErrInvalidObjectKey) {
				t.Errorf("Open(%q) = %v, want ErrInvalidObjectKey", key, err)
			}
			if _, err := s.Stat(ctx, key); !errors.Is(err, ErrInvalidObjectKey) {
				t.Errorf("Stat(%q) = %v, want ErrInvalidObjectKey", key, err)
			}
			if err := s.Delete(ctx, key); !errors.Is(err, ErrInvalidObjectKey) {
				t.Errorf("Delete(%q) = %v, want ErrInvalidObjectKey", key, err)
			}
		})
	}
}

// TestCOSStoreRoundTrip is the driver's real test, and it needs a bucket.
//
// It is skipped unless ONEXAI_COS_TEST_BUCKET_URL names one, with
// ONEXAI_COS_TEST_SECRET_ID and ONEXAI_COS_TEST_SECRET_KEY beside it. Set
// ONEXAI_COS_TEST_PREFIX too to exercise the namespace.
//
// The assertions are the ones LocalStore passes in TestBundlePutRoundTrip,
// because the bundle is what callers hold and a driver that satisfies it
// satisfies them: the two stores are interchangeable or the abstraction is
// decoration.
func TestCOSStoreRoundTrip(t *testing.T) {
	bucket := os.Getenv(cosTestEnvBucket)
	if bucket == "" {
		t.Skipf("set %s (with %s and %s) to run the COS round-trip",
			cosTestEnvBucket, cosTestEnvID, cosTestEnvKey)
	}
	prefix := os.Getenv(cosTestEnvPrefix)

	store, err := NewCOSStore(bucket, os.Getenv(cosTestEnvID), os.Getenv(cosTestEnvKey), "", prefix)
	if err != nil {
		t.Fatalf("NewCOSStore: %v", err)
	}

	b := NewBundle(store, nil)
	ctx := context.Background()
	data := testPNG(t, 4, 6)

	ref, err := b.Put(ctx, KindImage, "ou_cos_roundtrip", "screenshot.png", data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", ref.MimeType)
	}
	if ref.Size != int64(len(data)) {
		t.Errorf("Size = %d, want %d", ref.Size, len(data))
	}
	if ref.Width != 4 || ref.Height != 6 {
		t.Errorf("dimensions = %dx%d, want 4x6", ref.Width, ref.Height)
	}
	// The reference is the one thing that outlives the run, so the namespace it
	// resolved to is asserted rather than assumed.
	if prefix != "" && !strings.Contains(ref.URL, "/"+strings.Trim(prefix, "/")+"/") {
		t.Errorf("URL = %q, want it to carry the %q prefix", ref.URL, prefix)
	}

	t.Cleanup(func() { _ = b.Delete(context.Background(), ref) })

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
	if obj.MimeType != "image/png" {
		t.Errorf("Stat type = %q, want image/png", obj.MimeType)
	}

	// A key that was never written is a missing object, not an empty one: the
	// inliner branches on that to tell "expired and swept" from "unreachable".
	if _, err := store.Open(ctx, cosTestMissingKey); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Open of a missing key = %v, want ErrObjectNotFound", err)
	}

	if err := b.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := b.Stat(ctx, ref); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Stat after Delete = %v, want ErrObjectNotFound", err)
	}
	if err := b.Delete(ctx, ref); err != nil {
		t.Errorf("second Delete returned %v, want nil", err)
	}
}
