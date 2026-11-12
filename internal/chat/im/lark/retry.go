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

package lark

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
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

// Retrier wraps a Lark SDK client with two layers of resilience:
//
//  1. Transient retry: network-level failures (connection reset, timeout, EOF)
//     are retried with exponential backoff and jitter.
//  2. Tenant-token refresh: when a call fails with an invalid tenant access
//     token, a fresh token is fetched and the request is replayed on a
//     token-cache-disabled client carrying the fresh token.
//
// A Retrier is cheap to create but holds a lazily-built replay client, so one
// instance should be shared (e.g. per botBiz) rather than re-created per call.
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

// Do runs fn under both the transient and tenant-token retry layers. It is the
// common entry point for outbound Lark API calls.
func (r *Retrier) Do(ctx context.Context, op string, fn RequestFunc) error {
	return r.withTransientRetry(ctx, op, func() error {
		return r.withTenantTokenRetry(ctx, op, fn)
	})
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

	slog.Warn("retrying Lark request with fresh tenant access token", "operation", op)
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
		return "", fmt.Errorf("fetch tenant access token code=%d msg=%s", resp.Code, resp.Msg)
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
func isTenantAccessTokenInvalid(err error) bool {
	if err == nil {
		return false
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

// isTransientError reports whether err is a transient network error that
// warrants a retry (connection reset, timeout, EOF, etc.).
func isTransientError(err error) bool {
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
				slog.Info("transient retry succeeded", "operation", op, "attempt", attempt+1)
			}
			return nil
		}
		if !isTransientError(lastErr) {
			return lastErr
		}
		if attempt == maxTransientRetries {
			break
		}
		// Add jitter: up to +25% of delay to spread out concurrent retries.
		jitter := time.Duration(rand.Int64N(int64(delay / 4)))
		actualDelay := delay + jitter
		slog.Warn("transient error, retrying",
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
