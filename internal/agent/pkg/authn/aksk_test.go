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

package authn

import (
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // mirrors the contract under test.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/onexstack/onexai/internal/pkg/authz"
)

const (
	testAPIKey    = "svc-chat"
	testSecretKey = "s3cr3t"
)

func testProvider(secretID string) (string, bool) {
	if secretID != testAPIKey {
		return "", false
	}
	return testSecretKey, true
}

// sign builds the header set the middleware expects, over the given body. It is
// the client half of the contract, written independently of the middleware so a
// mistake in one is not mirrored by the other.
func sign(t *testing.T, method, path string, body []byte, ts time.Time) map[string]string {
	t.Helper()
	sum := md5.Sum(body) //nolint:gosec
	raw := fmt.Sprintf("%s\n%s\n%s\n%s\n%s",
		method, path, hex.EncodeToString(sum[:]),
		strconv.FormatInt(ts.UnixMilli(), 10), "nonce-1")
	mac := hmac.New(sha256.New, []byte(testSecretKey))
	mac.Write([]byte(raw))
	return map[string]string{
		headerAPIKey:    testAPIKey,
		headerTimestamp: strconv.FormatInt(ts.UnixMilli(), 10),
		headerNonce:     "nonce-1",
		headerSign:      hex.EncodeToString(mac.Sum(nil)),
	}
}

// newTestRouter mounts the middleware in front of a handler that records the
// subject it was given. subject() reads it back after a request has been served.
func newTestRouter(bypass bool) (*gin.Engine, func() *authz.Subject) {
	gin.SetMode(gin.TestMode)
	var seen *authz.Subject
	r := gin.New()
	r.POST("/v1/agent/ask", AKSKAuth(testProvider, bypass, nil), func(c *gin.Context) {
		if subj, ok := authz.SubjectFromContext(c.Request.Context()); ok {
			seen = &subj
		}
		c.Status(http.StatusOK)
	})
	return r, func() *authz.Subject { return seen }
}

func do(t *testing.T, r *gin.Engine, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ask", strings.NewReader(string(body)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

var body = []byte(`{"senderID":"u_alice","isSRE":true}`)

func TestAKSKAuthAcceptsAValidSignature(t *testing.T) {
	r, subject := newTestRouter(false)
	w := do(t, r, body, sign(t, http.MethodPost, "/v1/agent/ask", body, time.Now()))

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	seen := subject()
	if seen == nil {
		t.Fatal("the authenticated service was not attached as the request's subject")
	}
	if seen.Kind != authz.SubjectKindServiceAccount || seen.ServiceName != testAPIKey {
		t.Fatalf("got subject %+v, want a serviceaccount named %q", *seen, testAPIKey)
	}
}

func TestAKSKAuthRejectsAModifiedBody(t *testing.T) {
	// This is the property the identity assertions in the body depend on: a
	// signature that did not cover the body would let a captured request be
	// rewritten to claim SRE membership.
	r, _ := newTestRouter(false)
	headers := sign(t, http.MethodPost, "/v1/agent/ask", body, time.Now())
	tampered := []byte(`{"senderID":"u_alice","isSRE":true,"admin":true}`)

	if w := do(t, r, tampered, headers); w.Code != http.StatusUnauthorized {
		t.Fatalf("a tampered body must not verify, got status %d", w.Code)
	}
}

func TestAKSKAuthRejectsBadSignatures(t *testing.T) {
	now := time.Now()
	signed := sign(t, http.MethodPost, "/v1/agent/ask", body, now)

	tests := []struct {
		name        string
		mutate      func(map[string]string)
		wantMessage string
	}{
		{"wrong signature", func(h map[string]string) { h[headerSign] = strings.Repeat("0", 64) }, "signature verification failed"},
		{"unknown api key", func(h map[string]string) { h[headerAPIKey] = "svc-nobody" }, "invalid API key"},
		{"unparsable timestamp", func(h map[string]string) { h[headerTimestamp] = "not-a-number" }, "invalid timestamp format"},
		{"missing headers", func(h map[string]string) { delete(h, headerSign) }, "missing required auth headers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := map[string]string{}
			for k, v := range signed {
				headers[k] = v
			}
			tt.mutate(headers)

			w := do(t, r0(), body, headers)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("got status %d, want 401", w.Code)
			}
			if !strings.Contains(w.Body.String(), tt.wantMessage) {
				t.Fatalf("body %q does not mention %q", w.Body.String(), tt.wantMessage)
			}
		})
	}
}

func r0() *gin.Engine {
	r, _ := newTestRouter(false)
	return r
}

func TestAKSKAuthRejectsAnExpiredTimestamp(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	w := do(t, r0(), body, sign(t, http.MethodPost, "/v1/agent/ask", body, old))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401 for a request an hour old", w.Code)
	}
	if !strings.Contains(w.Body.String(), "timestamp") {
		t.Fatalf("body %q does not say the timestamp was the problem", w.Body.String())
	}
}

// TestAKSKAuthRejectsAReplayedRequest pins the nonce check: a correctly signed
// request is a bearer credential for its whole timestamp window, so a captured
// request replayed verbatim must be refused the second time even though its
// signature still verifies.
func TestAKSKAuthRejectsAReplayedRequest(t *testing.T) {
	r, _ := newTestRouter(false)
	headers := sign(t, http.MethodPost, "/v1/agent/ask", body, time.Now())

	if w := do(t, r, body, headers); w.Code != http.StatusOK {
		t.Fatalf("first request: got status %d, want 200 (body: %s)", w.Code, w.Body.String())
	}

	w := do(t, r, body, headers)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed request: got status %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "already been used") {
		t.Fatalf("body %q does not report the replay", w.Body.String())
	}
}

func TestAKSKAuthBypassPassesEverythingThrough(t *testing.T) {
	r, subject := newTestRouter(true)

	// No headers at all, and the handler still runs — that is the whole point
	// of the development bypass.
	if w := do(t, r, body, nil); w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 under bypass", w.Code)
	}
	if seen := subject(); seen != nil {
		t.Fatalf("bypass must not fabricate an identity, got %+v", *seen)
	}
}

func TestAKSKAuthRejectsAnUnknownKeyEvenWithAValidSignatureForIt(t *testing.T) {
	// A caller who signs correctly with a key the server does not know is still
	// not authenticated: the signature proves possession of *a* key, the lookup
	// decides whether that key may call.
	headers := sign(t, http.MethodPost, "/v1/agent/ask", body, time.Now())
	headers[headerAPIKey] = "svc-unknown"

	if w := do(t, r0(), body, headers); w.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401 for an unknown API key", w.Code)
	}
}

func TestAKSKAuthTreatsAPanickingProviderAsAnUnknownKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	panicking := func(string) (string, bool) { panic("corrupted secret row") }
	r.POST("/v1/agent/ask", AKSKAuth(panicking, false, nil), func(c *gin.Context) { c.Status(http.StatusOK) })

	w := do(t, r, body, sign(t, http.MethodPost, "/v1/agent/ask", body, time.Now()))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401 rather than a panic", w.Code)
	}
}
