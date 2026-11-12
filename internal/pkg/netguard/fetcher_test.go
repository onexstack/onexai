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

package netguard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubResolver answers a fixed set of names. It is the only way to exercise the
// address check without a DNS server: there is no name in a test that resolves
// to the metadata address.
type stubResolver struct {
	addrs map[string][]string
}

func (r stubResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	raw, ok := r.addrs[host]
	if !ok {
		return nil, errors.New("stub: no such host")
	}
	out := make([]netip.Addr, 0, len(raw))
	for _, s := range raw {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

// TestCheckAddrBlocks covers the address rules directly. They are the whole of
// the security decision, so they are tested as a table rather than reached
// through a request that would need a live hostile server to produce.
func TestCheckAddrBlocks(t *testing.T) {
	tests := []struct {
		addr  string
		block bool
		why   string
	}{
		{addr: "169.254.169.254", block: true, why: "cloud instance metadata"},
		{addr: "127.0.0.1", block: true, why: "IPv4 loopback"},
		{addr: "127.1.2.3", block: true, why: "the rest of 127/8"},
		{addr: "::1", block: true, why: "IPv6 loopback"},
		{addr: "10.0.0.1", block: true, why: "private"},
		{addr: "10.0.0.1", block: true, why: "private"},
		{addr: "192.168.1.1", block: true, why: "private"},
		{addr: "fc00::1", block: true, why: "unique local"},
		{addr: "fe80::1", block: true, why: "link-local"},
		{addr: "169.254.1.1", block: true, why: "link-local"},
		{addr: "0.0.0.0", block: true, why: "unspecified"},
		{addr: "::", block: true, why: "unspecified"},
		{addr: "224.0.0.1", block: true, why: "multicast"},
		{addr: "ff02::1", block: true, why: "IPv6 multicast"},
		// An IPv4-mapped IPv6 address is the same host as its IPv4 form, and a
		// check that looked at the 16-byte form alone would miss it.
		{addr: "::ffff:127.0.0.1", block: true, why: "IPv4 loopback in mapped form"},
		{addr: "::ffff:169.254.169.254", block: true, why: "metadata in mapped form"},
		{addr: "8.8.8.8", block: false},
		{addr: "1.1.1.1", block: false},
		{addr: "2606:4700:4700::1111", block: false},
	}

	f := NewFetcher(Options{})
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := f.checkAddr(netip.MustParseAddr(tt.addr))
			if tt.block {
				if !errors.Is(err, ErrBlockedTarget) {
					t.Fatalf("checkAddr(%s) = %v, want ErrBlockedTarget (%s)", tt.addr, err, tt.why)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkAddr(%s) = %v, want nil", tt.addr, err)
			}
		})
	}
}

// TestFetcherAllowsPrivateWhenConfigured pins the escape hatch: a deployment
// whose images live on an internal host must be able to opt in, and the opt-in
// must be the only way past the check.
func TestFetcherAllowsPrivateWhenConfigured(t *testing.T) {
	f := NewFetcher(Options{AllowPrivateHosts: true})
	if err := f.checkAddr(netip.MustParseAddr("127.0.0.1")); err != nil {
		t.Fatalf("checkAddr with AllowPrivateHosts = %v, want nil", err)
	}
}

func TestFetcherRejectsBlockedURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "metadata address literal", url: "http://169.254.169.254/latest/meta-data/"},
		{name: "loopback literal", url: "http://127.0.0.1:8080/x"},
		{name: "private literal", url: "http://10.0.0.1/x"},
		{name: "name that resolves to metadata", url: "http://evil.test/x"},
		{name: "name resolving to both public and private", url: "http://mixed.test/x"},
		{name: "non-http scheme", url: "file:///etc/passwd"},
		{name: "gopher scheme", url: "gopher://evil.test/x"},
		{name: "no host", url: "http:///x"},
	}

	f := NewFetcher(Options{Resolver: stubResolver{addrs: map[string][]string{
		"evil.test":  {"169.254.169.254"},
		"mixed.test": {"93.184.216.34", "10.0.0.1"},
	}}})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No network is involved: every one of these is refused before a
			// connection is attempted, which is the point of the check.
			if _, _, err := f.Get(context.Background(), tt.url); err == nil {
				t.Fatalf("Get(%s) succeeded, want a refusal", tt.url)
			}
		})
	}
}

// TestFetcherRefusesNameWithAnyBlockedAnswer pins the rule that one bad answer
// condemns the whole name. Taking the public answer would mean the next query
// could return only the private one.
func TestFetcherRefusesNameWithAnyBlockedAnswer(t *testing.T) {
	f := NewFetcher(Options{Resolver: stubResolver{addrs: map[string][]string{
		"mixed.test": {"93.184.216.34", "10.0.0.1"},
	}}})
	_, _, err := f.Get(context.Background(), "http://mixed.test/x")
	if !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("Get = %v, want ErrBlockedTarget", err)
	}
	if !strings.Contains(err.Error(), "10.0.0.1") {
		t.Errorf("the refusal does not name the address that caused it: %v", err)
	}
}

// TestFetcherRevalidatesRedirect is the redirect half of the defence: a public
// host that answers 302 to a blocked address must be refused, because the
// redirect is a second request to a URL the first check never saw.
//
// checkRedirect is called directly rather than through a live server. The
// decision under test is "does a redirect get validated", and the function
// wired into the client is exactly that decision.
func TestFetcherRevalidatesRedirect(t *testing.T) {
	f := NewFetcher(Options{Resolver: stubResolver{addrs: map[string][]string{
		"ok.test": {"93.184.216.34"},
	}}})

	newReq := func(raw string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, raw, nil)
		req.URL, _ = url.Parse(raw)
		return req
	}

	via := []*http.Request{newReq("http://ok.test/a")}
	if err := f.checkRedirect(newReq("http://ok.test/b"), via); err != nil {
		t.Fatalf("checkRedirect to another public host = %v, want nil", err)
	}

	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:8080/x",
		"http://evil.test/x",
	} {
		req := newReq(target)
		if err := f.checkRedirect(req, via); err == nil {
			t.Errorf("checkRedirect to %s succeeded, want a refusal", target)
		}
	}
}

// TestFetcherCapsRedirectChain pins that the chain is bounded, so a server
// answering with an endless redirect loop cannot make this process chase it.
func TestFetcherCapsRedirectChain(t *testing.T) {
	f := NewFetcher(Options{MaxRedirects: 2, Resolver: stubResolver{addrs: map[string][]string{
		"ok.test": {"93.184.216.34"},
	}}})
	req := httptest.NewRequest(http.MethodGet, "http://ok.test/x", nil)

	if err := f.checkRedirect(req, []*http.Request{req, req}); err != nil {
		t.Fatalf("the third hop should still be followed: %v", err)
	}
	if err := f.checkRedirect(req, []*http.Request{req, req, req}); err == nil {
		t.Fatal("the fourth hop was followed past the limit")
	}

	// A negative limit follows nothing at all.
	none := NewFetcher(Options{MaxRedirects: -1, Resolver: stubResolver{addrs: map[string][]string{
		"ok.test": {"93.184.216.34"},
	}}})
	if err := none.checkRedirect(req, []*http.Request{req}); err == nil {
		t.Fatal("MaxRedirects: -1 followed a redirect")
	}
}

// TestFetcherFetchesAndCapsBody exercises the whole path against a live server,
// which is only reachable because the loopback check is explicitly waived.
func TestFetcherFetchesAndCapsBody(t *testing.T) {
	body := strings.Repeat("a", 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := NewFetcher(Options{AllowPrivateHosts: true})
	data, contentType, err := f.Get(context.Background(), srv.URL+"/a.png")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != body {
		t.Errorf("Get returned %d bytes, want %d", len(data), len(body))
	}
	if !strings.HasPrefix(contentType, "image/png") {
		t.Errorf("content type = %q, want image/png", contentType)
	}

	// The ceiling is read one byte past, which is enough to tell the body did
	// not fit.
	capped := NewFetcher(Options{AllowPrivateHosts: true, MaxBytes: 100})
	if _, _, err := capped.Get(context.Background(), srv.URL+"/a.png"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Get under a 100-byte ceiling = %v, want ErrTooLarge", err)
	}

	// A non-200 is a failure, not an empty success.
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer notFound.Close()
	if _, _, err := f.Get(context.Background(), notFound.URL+"/missing"); err == nil {
		t.Error("Get of a 404 succeeded")
	}
}

// countingServer answers with statuses in order, recording how many requests it
// served.
func countingServer(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(calls.Add(1))
		status := statuses[len(statuses)-1]
		if n <= len(statuses) {
			status = statuses[n-1]
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestFetcherRetriesATransientFailure pins that a failure another attempt can
// fix is repeated once by default: a 502 from a CDN edge is the shape this
// covers, and it costs the caller a short pause rather than a dropped image.
func TestFetcherRetriesATransientFailure(t *testing.T) {
	srv, calls := countingServer(t, http.StatusBadGateway, http.StatusOK)

	f := NewFetcher(Options{AllowPrivateHosts: true, RetryBackoff: time.Millisecond})
	data, _, err := f.Get(context.Background(), srv.URL+"/a.png")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "ok" {
		t.Errorf("Get returned %q, want the second attempt's body", data)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("the server was asked %d times, want 2", got)
	}
}

// TestFetcherDoesNotRetryADefinitiveAnswer pins the other half: a 404 will not
// become a 200, so asking again only costs the caller time.
func TestFetcherDoesNotRetryADefinitiveAnswer(t *testing.T) {
	srv, calls := countingServer(t, http.StatusNotFound)

	f := NewFetcher(Options{AllowPrivateHosts: true, RetryBackoff: time.Millisecond})
	if _, _, err := f.Get(context.Background(), srv.URL+"/missing"); err == nil {
		t.Fatal("Get of a 404 succeeded")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("the server was asked %d times, want 1", got)
	}
}

// TestFetcherRetriesNothingWhenAsked pins the opt-out, since a caller that
// cannot afford a second request must be able to say so.
func TestFetcherRetriesNothingWhenAsked(t *testing.T) {
	srv, calls := countingServer(t, http.StatusBadGateway)

	f := NewFetcher(Options{AllowPrivateHosts: true, Retries: -1, RetryBackoff: time.Millisecond})
	if _, _, err := f.Get(context.Background(), srv.URL+"/a.png"); !errors.Is(err, ErrTransient) {
		t.Errorf("Get = %v, want ErrTransient", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("the server was asked %d times, want 1", got)
	}
}

// TestRedactURL pins that a presigned URL's query — where its credentials live
// — never reaches a log line whole.
func TestRedactURL(t *testing.T) {
	got := RedactURL("https://bucket.example.com/a.png?signature=SECRET&expires=1")
	if strings.Contains(got, "SECRET") {
		t.Errorf("RedactURL leaked the credential: %s", got)
	}
	if !strings.Contains(got, "bucket.example.com") {
		t.Errorf("RedactURL lost the identity a reader needs: %s", got)
	}
	if got := RedactURL("https://u:p@example.com/a"); strings.Contains(got, ":p@") {
		t.Errorf("RedactURL leaked the userinfo: %s", got)
	}
	if got := RedactURL("://not a url"); got != "<unparseable url>" {
		t.Errorf("RedactURL of an unparseable URL = %q", got)
	}
}
