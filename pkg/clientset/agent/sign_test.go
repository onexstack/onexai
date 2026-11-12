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

package agent

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // mirrors the contract under test.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"

	v1 "github.com/onexstack/onexai/pkg/api/agent/v1"
)

const (
	testAPIKey    = "oai-chat-apiserver"
	testSecretKey = "s3cr3t"
)

// verify recomputes the signature the way oai-agent-apiserver's middleware does, and
// compares. It is written independently of signer, so a mistake in one is not
// mirrored by the other.
func verify(t *testing.T, method, path string, body []byte, h http.Header) error {
	t.Helper()
	if h.Get("X-API-KEY") != testAPIKey {
		return fmt.Errorf("X-API-KEY = %q, want %q", h.Get("X-API-KEY"), testAPIKey)
	}
	ts := h.Get("X-TIMESTAMP")
	if ts == "" {
		return fmt.Errorf("X-TIMESTAMP is missing")
	}
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("X-TIMESTAMP is not milliseconds: %w", err)
	}
	if skew := time.Since(time.UnixMilli(ms)); skew > time.Minute || skew < -time.Minute {
		return fmt.Errorf("X-TIMESTAMP is %v away from now", skew)
	}

	sum := md5.Sum(body) //nolint:gosec
	raw := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", method, path, hex.EncodeToString(sum[:]), ts, h.Get("X-NONCE"))
	mac := hmac.New(sha256.New, []byte(testSecretKey))
	mac.Write([]byte(raw))
	if want := hex.EncodeToString(mac.Sum(nil)); !hmac.Equal([]byte(h.Get("X-SIGN")), []byte(want)) {
		return fmt.Errorf("X-SIGN does not verify; the body sent is not the body signed")
	}
	return nil
}

// capture serves one request, recording its path, raw body and headers.
type capture struct {
	method string
	path   string
	body   []byte
	header http.Header
}

func newSignedClient(t *testing.T, c *capture) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c.method, c.path, c.body, c.header = r.Method, r.URL.Path, raw, r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	opts := genericoptions.NewRestyOptions()
	opts.Endpoint = srv.URL
	return NewWithSigning(opts, testAPIKey, testSecretKey), srv
}

func TestSignedClientSignsTheExactBodyItSends(t *testing.T) {
	var got capture
	c, _ := newSignedClient(t, &got)

	req := &v1.AgentStopRequest{RunID: "run-1"}
	if err := c.Stop(context.Background(), req.GetRunID()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got.path != "/v1/agent/stop" || got.method != http.MethodPost {
		t.Fatalf("got %s %s, want POST /v1/agent/stop", got.method, got.path)
	}
	if err := verify(t, got.method, got.path, got.body, got.header); err != nil {
		t.Fatal(err)
	}

	// The body on the wire must still be the request, byte for byte, not just
	// something the signature happened to cover.
	var roundTrip v1.AgentStopRequest
	if err := json.Unmarshal(got.body, &roundTrip); err != nil {
		t.Fatalf("body is not the request JSON: %v (%s)", err, got.body)
	}
	if roundTrip.GetRunID() != "run-1" {
		t.Fatalf("got runID %q, want run-1", roundTrip.GetRunID())
	}
}

// TestSignedClientSignsTheAuthzDecisionBody is the regression test for a call
// that signed one body and sent another.
//
// The authorization decision is the one POST whose body is built by the caller
// rather than passed straight through, and it was written as "prepare with no
// body, then SetBody" — so the signature covered md5("") while the wire carried
// the decision request. The server recomputes over what it receives, so the
// answer was always 401 "signature verification failed", and the symptom was
// /agent refusing every agent with "授权判定失败". The other POSTs passed their
// body to prepare and were unaffected, which is why this never showed up
// anywhere else.
func TestSignedClientSignsTheAuthzDecisionBody(t *testing.T) {
	var got capture
	c, _ := newSignedClient(t, &got)

	if _, _, err := c.CheckAgentAccess(context.Background(), "go.kong", "onex-x-max-tier"); err != nil {
		t.Fatalf("CheckAgentAccess: %v", err)
	}

	if got.path != "/v1/authz/decisions" || got.method != http.MethodPost {
		t.Fatalf("got %s %s, want POST /v1/authz/decisions", got.method, got.path)
	}
	if err := verify(t, got.method, got.path, got.body, got.header); err != nil {
		t.Fatal(err)
	}

	// And the body on the wire is still the decision request.
	var roundTrip AgentAccessRequest
	if err := json.Unmarshal(got.body, &roundTrip); err != nil {
		t.Fatalf("body is not the decision request JSON: %v (%s)", err, got.body)
	}
	if roundTrip.SubjectID != "go.kong" || roundTrip.AgentName != "onex-x-max-tier" {
		t.Fatalf("got %+v, want the subject and agent that were asked about", roundTrip)
	}
}

func TestSignedClientSignsBodylessCallsToo(t *testing.T) {
	var got capture
	c, _ := newSignedClient(t, &got)

	if err := c.ClearSession(context.Background(), "sess-1"); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}

	if got.path != "/v1/chat/sessions/sess-1" || got.method != http.MethodDelete {
		t.Fatalf("got %s %s, want DELETE /v1/chat/sessions/sess-1", got.method, got.path)
	}
	if err := verify(t, got.method, got.path, got.body, got.header); err != nil {
		t.Fatal(err)
	}
}

func TestUnsignedClientSendsNoSignature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-SIGN") != "" {
			t.Errorf("an unsigned client sent X-SIGN")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	opts := genericoptions.NewRestyOptions()
	opts.Endpoint = srv.URL
	if err := NewForConfig(opts).Stop(context.Background(), "run-1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestHalfConfiguredPairFallsBackToUnsigned(t *testing.T) {
	// A key with no secret cannot produce a valid signature. Sending an
	// unsigned request fails loudly against the server; sending a bogus
	// signature would look like a credential problem instead of a config one.
	if s := newSigner("key-only", ""); s != nil {
		t.Fatal("a half-configured pair must not build a signer")
	}
}
