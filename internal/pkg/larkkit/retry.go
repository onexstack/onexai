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

// Package larkkit holds the Lark/Feishu plumbing shared by the services that
// talk to the open platform: token-expiry replay, transient-network retry, and
// an SDK logger adapter.
//
// This is the shared home for what internal/chat/im/lark/retry.go
// currently implements privately. The two are deliberately not merged yet;
// chat keeps its copy until its behaviour can be re-verified against this
// one, rather than both changing at once.
package larkkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// RequestFunc is a Lark API call bound to a client. The client and request
// options are injected by the Retrier so a stale token can be transparently
// replaced by a fresh one and replayed on a cache-disabled client.
type RequestFunc func(client *lark.Client, options ...larkcore.RequestOptionFunc) error

// Lark business error codes the retry layers act on.
const (
	// CodeFrequencyLimit is the platform's request-frequency refusal. It arrives
	// with an HTTP 200 and a business code, so only the code identifies it — and
	// it means "later" rather than "never", which is why it is retried. Exported
	// because it is part of the platform's contract rather than an implementation
	// detail of this package.
	CodeFrequencyLimit = 99991400
	// CodeTenantTokenInvalid is an expired tenant access token, which the
	// tenant-token layer replays.
	CodeTenantTokenInvalid = 99991663
)

// APIError is a Lark/Bitable answer that carried a failure code.
//
// It exists so the retry layers can tell apart the two kinds of failure a
// response-embedded error can mean: a refusal that another attempt may survive
// (throttling, a server-side 5xx) and one that never will (a permission change,
// a value the API rejects). Without the distinction every such answer reads as
// permanent, and a rate-limited pass fails outright instead of backing off.
type APIError struct {
	// Op names the call, for the log line the retry layer emits.
	Op string
	// Code is the platform's business error code.
	Code int
	// Status is the HTTP status, 0 when the SDK did not surface one.
	Status int
	// Msg is the platform's message.
	Msg string
}

// NewAPIError builds the typed error a call site returns when a response
// reports failure.
func NewAPIError(op string, code, status int, message string) *APIError {
	return &APIError{Op: op, Code: code, Status: status, Msg: message}
}

// Error renders the operation, both codes and the message, which is what the
// operation log needs to explain a refusal.
func (e *APIError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s failed: code=%d status=%d msg=%s", e.Op, e.Code, e.Status, e.Msg)
	}
	return fmt.Sprintf("%s failed: code=%d msg=%s", e.Op, e.Code, e.Msg)
}

// Retrier wraps a Lark SDK client with two layers of resilience:
//
//  1. Transient retry: network-level failures (connection reset, timeout, EOF)
//     are retried with exponential backoff and jitter.
//  2. Tenant-token refresh: when a call fails with an invalid tenant access
//     token, a fresh token is fetched and the request replayed on a
//     token-cache-disabled client carrying the fresh token.
//
// The second layer is what makes a long-running sync survivable: a full pass
// over a large table outlives the tenant token, and without replay every
// remaining call in the pass fails.
//
// A Retrier is cheap to create but holds a lazily-built replay client, so one
// instance should be shared rather than re-created per call.
type Retrier struct {
	client    *lark.Client
	appID     string
	appSecret string

	replayMu     sync.Mutex
	replayClient *lark.Client
}

// NewRetrier builds a Retrier over the given SDK client and app credentials.
// The credentials are required to fetch a fresh tenant token on expiry.
func NewRetrier(client *lark.Client, appID, appSecret string) *Retrier {
	return &Retrier{client: client, appID: appID, appSecret: appSecret}
}

// Client returns the underlying SDK client, for call sites that do not need the
// retry layers (websocket setup, for instance).
func (r *Retrier) Client() *lark.Client { return r.client }

// Do runs fn under both the transient and tenant-token retry layers. It is the
// common entry point for outbound Lark API calls.
func (r *Retrier) Do(ctx context.Context, op string, fn RequestFunc) error {
	return r.withTransientRetry(ctx, op, func() error {
		return r.withTenantTokenRetry(ctx, op, fn)
	})
}

// DoNonIdempotent runs fn under the tenant-token layer only, never the
// transient-network retry.
//
// A create whose response was lost may already have been applied on the
// server, and replaying the same body would create a second record — a silent
// duplicate, which is strictly worse than a failure the caller can see and
// reconcile on its next pass. Token replay is kept because an invalid token
// means the request was rejected rather than applied, so that attempt is known
// to have had no effect.
//
// The consequence for a call after this one: a throttled or 5xx answer is
// reported instead of retried in-process, so the caller's next tick is the
// retry. That is the right cadence for a write that cannot be replayed safely.
func (r *Retrier) DoNonIdempotent(ctx context.Context, op string, fn RequestFunc) error {
	return r.withTenantTokenRetry(ctx, op, fn)
}

// withTenantTokenRetry runs fn once on the main client; if the error indicates
// an invalid tenant access token, it fetches a fresh token and replays on a
// cache-disabled client with the fresh token injected.
func (r *Retrier) withTenantTokenRetry(ctx context.Context, op string, fn RequestFunc) error {
	err := fn(r.client)
	if !isTenantAccessTokenInvalid(err) {
		return err
	}

	freshToken, refreshErr := r.fetchFreshTenantAccessToken(ctx)
	if refreshErr != nil {
		return fmt.Errorf("%s failed after token refresh attempt: %w (original error: %v)", op, refreshErr, err)
	}

	slog.WarnContext(ctx, "retrying Lark request with fresh tenant access token", "operation", op)
	return fn(r.replayAPIClient(), larkcore.WithTenantAccessToken(freshToken))
}

// fetchFreshTenantAccessToken obtains a new tenant access token using the app
// credentials via the self-built app token endpoint.
func (r *Retrier) fetchFreshTenantAccessToken(ctx context.Context) (string, error) {
	resp, err := r.replayAPIClient().GetTenantAccessTokenBySelfBuiltApp(ctx, &larkcore.SelfBuiltTenantAccessTokenReq{
		AppID:     r.appID,
		AppSecret: r.appSecret,
	})
	if err != nil {
		return "", fmt.Errorf("fetch tenant access token: %w", err)
	}
	if !resp.Success() {
		return "", NewAPIError("fetch tenant access token", resp.Code, 0, resp.Msg)
	}
	if strings.TrimSpace(resp.TenantAccessToken) == "" {
		return "", fmt.Errorf("fetch tenant access token returned empty token")
	}
	return resp.TenantAccessToken, nil
}

// replayAPIClient lazily builds a cache-disabled SDK client used to replay
// requests with an explicitly injected fresh token. Reusing the main client
// would keep serving the cached (stale) token.
func (r *Retrier) replayAPIClient() *lark.Client {
	r.replayMu.Lock()
	defer r.replayMu.Unlock()
	if r.replayClient == nil {
		r.replayClient = lark.NewClient(r.appID, r.appSecret, lark.WithEnableTokenCache(false))
	}
	return r.replayClient
}

// isTenantAccessTokenInvalid reports whether err indicates an expired/invalid
// tenant access token (error code 99991663 or the "invalid access token"
// message), which warrants a token refresh.
//
// The typed error is checked first because a call site that reports the
// platform's code gets an exact answer; the string match stays as the fallback
// for errors built by the SDK itself, which this package does not construct.
func isTenantAccessTokenInvalid(err error) bool {
	if err == nil {
		return false
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		return apiErr.Code == CodeTenantTokenInvalid
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "99991663") || strings.Contains(message, "invalid access token")
}

// Transient retry constants for network-level failures.
const (
	maxTransientRetries    = 3
	transientRetryInitial  = 500 * time.Millisecond
	transientRetryMaxDelay = 5 * time.Second
)

// IsTransientError reports whether err is a transient error that warrants a
// retry: a network-level failure, or an answer that says "try again" (a rate
// limit or a server-side error).
//
// Anything else is permanent. That distinction matters more than it looks: the
// retry loop below gives up on the first non-transient error, so classifying a
// permission refusal as transient would turn one revoked grant into a retry
// storm, while classifying a 429 as permanent makes a throttled pass fail
// instead of backing off.
func IsTransientError(err error) bool {
	if err == nil {
		return false
	}
	// Typed syscall checks — more robust than string matching.
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	// net.Error covers timeouts and temporary errors from the stdlib.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// EOF usually means the server closed the connection mid-response.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// A response-embedded failure: transient only when the platform said so.
	// The switch is exhaustive on purpose — an unrecognised status is permanent,
	// because retrying a refusal the platform did not label as retryable is how
	// a sync service turns one bad request into an outage.
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		switch apiErr.Status {
		case http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return true
		}
		return apiErr.Code == CodeFrequencyLimit
	}
	// Unwrapped string checks for common transient symptoms that may appear in
	// wrapped Feishu SDK errors.
	message := err.Error()
	for _, substr := range []string{
		"connection reset by peer",
		"broken pipe",
		"i/o timeout",
		"TLS handshake timeout",
		"server misbehaving",
		"connection refused",
	} {
		if strings.Contains(message, substr) {
			return true
		}
	}
	return false
}

// withTransientRetry wraps fn with exponential-backoff retry on transient
// network errors. Non-transient errors are returned immediately. Jitter (up to
// +25% of delay) is added to prevent thundering-herd retries.
func (r *Retrier) withTransientRetry(ctx context.Context, op string, fn func() error) error {
	var lastErr error
	delay := transientRetryInitial
	for attempt := 0; attempt <= maxTransientRetries; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			if attempt > 0 {
				slog.InfoContext(ctx, "transient retry succeeded", "operation", op, "attempt", attempt+1)
			}
			return nil
		}
		if !IsTransientError(lastErr) {
			return lastErr
		}
		if attempt == maxTransientRetries {
			break
		}
		// Add jitter: up to +25% of delay to spread out concurrent retries.
		jitter := time.Duration(rand.Int64N(int64(delay / 4)))
		actualDelay := delay + jitter
		slog.WarnContext(ctx, "transient error, retrying",
			"operation", op,
			"attempt", attempt+1,
			"max_retries", maxTransientRetries,
			"delay", actualDelay,
			"error", lastErr,
		)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s retry cancelled: %w (last error: %v)", op, ctx.Err(), lastErr)
		case <-time.After(actualDelay):
		}
		delay = min(delay*2, transientRetryMaxDelay)
	}
	return fmt.Errorf("%s failed after %d retries: %w", op, maxTransientRetries, lastErr)
}
