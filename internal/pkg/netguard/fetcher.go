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

// Package netguard fetches a URL only after proving the address it resolves to
// is one this service is allowed to reach.
//
// # Why this exists
//
// An image reference may arrive as a plain URL: from a client, from an upstream
// service, or from a data row written before this service stored its own bytes.
// Fetching such a URL server-side is a request the caller writes and this
// process makes, which is the shape of a server-side request forgery: a URL
// naming `http://169.254.169.254/latest/meta-data/` asks this process, from
// inside the network, for the instance's credentials, and the answer comes back
// in the response body.
//
// # Why a URL check is not enough
//
// Validating the hostname and then dialling it resolves the name twice, and the
// second answer is not required to be the first. A host that resolves to a
// public address when checked and to a private one when dialled — DNS
// rebinding — passes every check made on the first lookup. So the dial is
// pinned: the name is resolved once, every address it yields is checked, and
// the connection is made to an address from that vetted set rather than to the
// name again.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// Sentinel errors.
var (
	// ErrBlockedTarget means the URL resolves to an address this service may
	// not reach.
	ErrBlockedTarget = errors.New("netguard: blocked target")
	// ErrTooLarge means the response exceeds the configured ceiling.
	ErrTooLarge = errors.New("netguard: response is too large")
	// ErrBadURL means the URL is not one this fetcher will make a request for.
	ErrBadURL = errors.New("netguard: unusable URL")
	// ErrTransient means the failure is the kind another attempt can fix — a
	// 5xx, or a 429. It is a marker rather than a promise: a retry may fail the
	// same way, which is why the attempts are bounded.
	ErrTransient = errors.New("netguard: transient upstream failure")
)

// Defaults.
const (
	// DefaultMaxBytes bounds a fetched body. An image is capped further
	// upstream; this is the backstop that keeps a hostile endpoint from
	// streaming until the process runs out of memory.
	DefaultMaxBytes = 12 << 20 // 12 MiB
	// DefaultTimeout bounds one whole fetch, redirects included.
	DefaultTimeout = 10 * time.Second
	// DefaultMaxRedirects bounds the chain. Each hop is re-validated, so this
	// bounds how many times a hostile server can make us validate.
	DefaultMaxRedirects = 3
	// DefaultRetries bounds how many times a fetch is repeated after a failure
	// another attempt can fix. One, rather than none: fetching an image is an
	// idempotent GET, and the failures this covers — a 502 from a CDN edge, a
	// reset connection — are the kind that succeed immediately afterwards.
	DefaultRetries = 1
	// DefaultRetryBackoff is the pause before a retry. It is short because the
	// caller is waiting on an image mid-conversation, and long enough that a
	// server which just refused is not asked again in the same instant.
	DefaultRetryBackoff = 250 * time.Millisecond
)

// Options configures a Fetcher.
type Options struct {
	// AllowPrivateHosts permits targets this package otherwise blocks —
	// loopback, private, and link-local addresses.
	//
	// It is an explicit opt-in rather than a mode, because a deployment whose
	// images live on an internal host is a deployment that has decided these
	// requests are its own, and that decision should be visible in a config
	// file rather than inferred. Default false: a URL that reaches a private
	// address is refused, and the refusal is reported.
	AllowPrivateHosts bool
	// MaxBytes bounds the body. Zero uses DefaultMaxBytes.
	MaxBytes int64
	// Timeout bounds one fetch. Zero uses DefaultTimeout.
	Timeout time.Duration
	// MaxRedirects bounds the redirect chain. Zero uses DefaultMaxRedirects;
	// a negative value follows no redirects at all.
	MaxRedirects int
	// Retries bounds how many times a fetch is repeated after a failure another
	// attempt can fix. Zero uses DefaultRetries; a negative value retries
	// nothing, which is how a caller opts out.
	Retries int
	// RetryBackoff is the pause before a retry. Zero uses DefaultRetryBackoff.
	RetryBackoff time.Duration
	// Resolver is the name resolver to use. Nil uses the system resolver.
	//
	// It is a seam for tests, which need a name that answers with an address
	// they chose — there is no other way to exercise the rebinding defence
	// without a DNS server.
	Resolver Resolver
}

// Resolver is the slice of the name resolver this package uses.
//
// It is an interface rather than a *net.Resolver so a test can answer with an
// address it chose. *net.Resolver satisfies it as it stands, so the production
// path needs no adapter.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Fetcher makes the vetted requests. It is safe for concurrent use.
type Fetcher struct {
	allowPrivate bool
	maxBytes     int64
	redirects    int
	retries      int
	backoff      time.Duration
	client       *http.Client
	resolver     Resolver
}

// NewFetcher builds a Fetcher.
func NewFetcher(opts Options) *Fetcher {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	redirects := opts.MaxRedirects
	switch {
	case redirects == 0:
		// The zero value means "not configured", which is the common case: a
		// caller that never thought about redirects gets the default rather
		// than silently following none, which would look like a broken host.
		redirects = DefaultMaxRedirects
	case redirects < 0:
		// A negative value is how "follow none" is spelled, because the zero
		// value is already taken by the default.
		redirects = -1
	}

	// The same convention, for the same reason: zero is "not configured", so a
	// caller that wants no retries says so with a negative number.
	retries := opts.Retries
	switch {
	case retries == 0:
		retries = DefaultRetries
	case retries < 0:
		retries = 0
	}
	backoff := opts.RetryBackoff
	if backoff <= 0 {
		backoff = DefaultRetryBackoff
	}

	f := &Fetcher{
		allowPrivate: opts.AllowPrivateHosts,
		maxBytes:     maxBytes,
		redirects:    redirects,
		retries:      retries,
		backoff:      backoff,
		resolver:     opts.Resolver,
	}
	f.client = &http.Client{
		Timeout:       timeout,
		CheckRedirect: f.checkRedirect,
		Transport: &http.Transport{
			// The dial is where the address is chosen, so it is where the
			// address is checked. Setting it here rather than reusing
			// http.DefaultTransport is the whole of the rebinding defence.
			DialContext:           f.dialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			// A proxy would make the request from somewhere this check did not
			// examine, which is a different address from the one validated.
			Proxy: nil,
		},
	}
	return f
}

// Client returns the vetted HTTP client. It is the same transport and redirect
// policy Get uses, exposed for callers that make their own requests — a model
// endpoint configured by a user is one. The client has no overall Timeout
// beyond the one Options.Timeout configured (the fetcher's own default is
// short because it serves image downloads); a streaming model call must set a
// timeout of its own on the request context.
func (f *Fetcher) Client() *http.Client {
	return f.client
}

// Get fetches a URL and returns its body and media type.
//
// The response is read whole and bounded: the callers of this method are about
// to hold the bytes in memory anyway, and a streaming interface would only move
// the bound to each of them.
//
// A transient failure is retried a bounded number of times. Only failures
// another attempt can fix are repeated — see retryable — so a refused or
// oversized target is answered at once, and the whole call is still bounded by
// the client timeout per attempt.
func (f *Fetcher) Get(ctx context.Context, rawURL string) ([]byte, string, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		data, contentType, err := f.getOnce(ctx, rawURL)
		if err == nil {
			return data, contentType, nil
		}
		lastErr = err
		if attempt >= f.retries || !retryable(err) {
			return nil, "", lastErr
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(f.backoff):
		}
	}
}

// getOnce makes one attempt.
func (f *Fetcher) getOnce(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrBadURL, err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrBadURL, err)
	}
	if err := f.checkURL(ctx, u); err != nil {
		return nil, "", err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return nil, "", fmt.Errorf("%w: %s returned HTTP %d", ErrTransient, u.Host, resp.StatusCode)
		}
		return nil, "", fmt.Errorf("netguard: %s returned HTTP %d", u.Host, resp.StatusCode)
	}

	// One byte past the ceiling is read, which is enough to tell whether the
	// body fit without draining the rest of it.
	data, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("netguard: read %s: %w", u.Host, err)
	}
	if int64(len(data)) > f.maxBytes {
		return nil, "", fmt.Errorf("%w: %s sent more than %d bytes", ErrTooLarge, u.Host, f.maxBytes)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// retryable reports whether another attempt could succeed.
//
// The set is deliberately small. A retry costs the caller time and the far end
// another request, so it is spent only where the failure is about the attempt
// rather than about the request: a 5xx or a 429, or a transport failure that is
// not the caller's own cancellation. A blocked target, a malformed URL, an
// oversized body and an ordinary 4xx are definitive — asking again produces the
// same answer, more slowly.
func retryable(err error) bool {
	if errors.Is(err, ErrTransient) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrBadURL) || errors.Is(err, ErrBlockedTarget) || errors.Is(err, ErrTooLarge) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// checkRedirect re-validates every hop.
//
// A redirect is a second request to a URL the original did not name, so it is a
// second opportunity to reach an address the first check refused. Validating
// only the original URL would let a public host answer `302
// http://169.254.169.254/…` and have this process follow it.
func (f *Fetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > f.redirects {
		return fmt.Errorf("netguard: stopped after %d redirects", f.redirects)
	}
	return f.checkURL(req.Context(), req.URL)
}

// checkURL refuses a URL this fetcher will not request.
func (f *Fetcher) checkURL(ctx context.Context, u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q (want http or https)", ErrBadURL, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: no host", ErrBadURL)
	}
	// The name is resolved here, and the answer is thrown away: the dial
	// resolves again and pins its own answer. This check exists so a refusal
	// reads as "that host is not allowed" rather than as a connection error
	// from inside the dialer, where the reason is much harder to see.
	_, err := f.vettedAddrs(ctx, u.Hostname())
	return err
}

// dialContext connects to a vetted address rather than to the name again.
//
// # The two checks here are not the same check twice
//
// vettedAddrs resolves the name and refuses if any answer is a blocked address.
// Choosing an address from its returned set is what makes the dial resistant to
// rebinding: the connection goes to an address that was examined, not to
// whatever the name answers with now.
//
// The control function then checks the address the kernel is about to connect
// to. It is the backstop for the paths that do not go through the loop below —
// an address literal in the URL, or a resolver that answered through a route
// this function did not take — and it is the last point at which the address is
// still visible before a connection exists.
func (f *Fetcher) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("netguard: dial address %q: %w", address, err)
	}

	addrs, err := f.vettedAddrs(ctx, host)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			addrPort, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: dial address %q: %v", ErrBlockedTarget, address, err)
			}
			return f.checkAddr(addrPort.Addr())
		},
	}

	var lastErr error
	for _, addr := range addrs {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: %s resolved to no usable address", ErrBlockedTarget, host)
	}
	return nil, lastErr
}

// vettedAddrs resolves host and returns the addresses that may be dialled.
//
// It refuses the whole name when **any** answer is blocked, rather than
// filtering the blocked ones out. A name that answers with both a public and a
// private address is a name whose operator is not in control of where it
// points, and taking the public answer means the next query could return only
// the private one.
func (f *Fetcher) vettedAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	if host == "" {
		return nil, fmt.Errorf("%w: no host", ErrBadURL)
	}

	// A literal address is not resolved — there is nothing to resolve — and it
	// is checked directly. It is the case a name-based check would miss.
	if addr, err := netip.ParseAddr(host); err == nil {
		if err := f.checkAddr(addr); err != nil {
			return nil, err
		}
		return []netip.Addr{addr.Unmap()}, nil
	}

	resolver := f.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("netguard: resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: %s resolved to nothing", ErrBlockedTarget, host)
	}

	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if err := f.checkAddr(ip); err != nil {
			return nil, err
		}
		out = append(out, ip.Unmap())
	}
	return out, nil
}

// checkAddr refuses an address that is not one this fetcher may reach.
func (f *Fetcher) checkAddr(addr netip.Addr) error {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlockedTarget)
	}
	if f.allowPrivate {
		return nil
	}
	switch {
	case addr.IsLoopback():
		return refuse(addr, "loopback")
	case addr.IsUnspecified():
		return refuse(addr, "unspecified")
	case addr.IsLinkLocalUnicast():
		return refuse(addr, "link-local")
	case addr.IsLinkLocalMulticast(), addr.IsMulticast():
		return refuse(addr, "multicast")
	case addr.IsInterfaceLocalMulticast():
		return refuse(addr, "interface-local multicast")
	case addr.IsPrivate():
		return refuse(addr, "private")
	}
	return nil
}

// refuse renders a blocked address with the reason, so a log line says which
// rule caught it.
//
// 169.254.169.254 is named in the message because it is the address the whole
// package exists for — the cloud instance metadata service — and someone
// reading a refusal should not have to look up which range it falls in to
// recognise it.
func refuse(addr netip.Addr, reason string) error {
	if addr.String() == "169.254.169.254" {
		return fmt.Errorf("%w: %s is the cloud instance metadata address", ErrBlockedTarget, addr)
	}
	return fmt.Errorf("%w: %s is a %s address", ErrBlockedTarget, addr, reason)
}

// RedactURL renders a URL for a log line without its query string.
//
// A presigned URL carries its credentials in the query — that is what makes it
// work without a header — so logging one whole writes a working credential to
// the log. The host and path are what a reader needs to recognise the request;
// the query is what an attacker needs to use it.
func RedactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "<unparseable url>"
	}
	if u.RawQuery != "" {
		u.RawQuery = "…"
	}
	if u.User != nil {
		u.User = url.User("…")
	}
	return u.String()
}
