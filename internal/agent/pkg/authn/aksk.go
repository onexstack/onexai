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

// Package authn holds oai-agent-apiserver's inbound authentication: the AK/SK
// signature check that every /v1 caller (in practice oai-chat-apiserver) must pass.
//
// It is separate from pkg/authz. Authentication answers "which service is
// calling"; authorization answers "may it do this". The first is a boundary of
// the process, the second a property of the agent run — and only the first
// could ever be a gin middleware.
package authn

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // MD5 here is a body digest inside an HMAC-signed string, not a security primitive on its own.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/authn/method"
	"github.com/onexstack/onexai/internal/pkg/authz"
	"github.com/onexstack/onexai/internal/pkg/errno"
)

const (
	headerAPIKey    = "X-API-KEY"
	headerTimestamp = "X-TIMESTAMP"
	headerNonce     = "X-NONCE"
	headerSign      = "X-SIGN"

	// maxTimestampSkew is the maximum allowed difference between the request
	// timestamp and the server time. It bounds replay of a captured request.
	maxTimestampSkew = 5 * time.Minute
)

// SecretProviderFunc returns the secret key for a key id, and whether it is
// known. A provider that cannot answer must return false rather than an empty
// key: an empty key would still verify against a signature computed with it.
type SecretProviderFunc func(secretID string) (secretKey string, exists bool)

// AKSKAuth returns a gin middleware that validates AK/SK signed requests.
//
// Signature algorithm (must match the caller's implementation):
//
//	bodyMD5 = hex( MD5(requestBody) )
//	rawStr  = METHOD + "\n" + PATH + "\n" + bodyMD5 + "\n" + X-TIMESTAMP + "\n" + X-NONCE
//	X-SIGN  = hex( HMAC-SHA256(secretKey, rawStr) )
//
// The body is part of the signed string on purpose: the request body carries
// the caller's identity assertions (sender id, SRE membership), so a signature
// that covered only the URL would let anyone who captured a request rewrite who
// it says it is.
//
// The nonce is claimed from nonces only after the signature verifies, so an
// unauthenticated caller cannot burn a legitimately-signed caller's nonce. A nil
// store is replaced with a process-local one rather than disabling replay
// detection; see NewNonceStore for what that does and does not protect.
func AKSKAuth(provider SecretProviderFunc, bypassAuth bool, nonces NonceStore) gin.HandlerFunc {
	nonces = normalizeNonces(nonces)

	return func(c *gin.Context) {
		if bypassAuth {
			c.Request = c.Request.WithContext(method.With(c.Request.Context(), method.Bypass))
			c.Next()
			return
		}

		if !AuthenticateAKSK(c, provider, nonces) {
			return
		}
		c.Next()
	}
}

// AuthenticateAKSK performs the AK/SK check and stores the authenticated
// service as the request's authorization subject. It writes the failure
// response and aborts the request when the check fails, and never advances the
// chain: callers decide whether to call c.Next().
//
// The split exists so a composite middleware can select a credential type
// without one middleware calling c.Next() inside another.
func AuthenticateAKSK(c *gin.Context, provider SecretProviderFunc, nonces NonceStore) bool {
	if nonces == nil {
		nonces = newMemoryNonceStore()
	}

	ctx := c.Request.Context()

	// 1. Extract the required headers.
	apiKey := c.GetHeader(headerAPIKey)
	timestampStr := c.GetHeader(headerTimestamp)
	nonce := c.GetHeader(headerNonce)
	sign := c.GetHeader(headerSign)

	if apiKey == "" || timestampStr == "" || nonce == "" || sign == "" {
		slog.WarnContext(ctx, "AK/SK auth: missing required headers",
			"api_key_present", apiKey != "",
			"timestamp_present", timestampStr != "",
			"nonce_present", nonce != "",
			"sign_present", sign != "",
		)
		unauthorized(c, "missing required auth headers")
		return false
	}

	// 2. Look up the key. Guard against a panicking provider (a corrupted
	// secret row, say) so a bad record cannot crash the request instead of
	// failing it cleanly.
	secretKey, exists := safeProvide(ctx, provider, apiKey)
	if !exists {
		slog.WarnContext(ctx, "AK/SK auth: unknown API key", "api_key", apiKey)
		unauthorized(c, "invalid API key")
		return false
	}

	// 3. Validate the timestamp.
	tsMs, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		slog.WarnContext(ctx, "AK/SK auth: invalid timestamp format", "timestamp", timestampStr)
		unauthorized(c, "invalid timestamp format")
		return false
	}
	requestTime := time.UnixMilli(tsMs)
	if skew := time.Since(requestTime); math.Abs(float64(skew)) > float64(maxTimestampSkew) {
		slog.WarnContext(ctx, "AK/SK auth: timestamp out of range", "skew", skew)
		unauthorized(c, "timestamp expired or too far in the future")
		return false
	}

	// 4. Read the body and restore it for the downstream handlers.
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		slog.ErrorContext(ctx, "AK/SK auth: failed to read request body", "error", err)
		core.WriteResponse(c, nil, errno.ErrBind.WithMessage("failed to read request body"))
		c.Abort()
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// 5-7. Recompute the signature over the same string the caller signed.
	//nolint:gosec // see the md5 import note.
	sum := md5.Sum(bodyBytes)
	rawString := fmt.Sprintf("%s\n%s\n%s\n%s\n%s",
		c.Request.Method,
		c.Request.URL.Path,
		hex.EncodeToString(sum[:]),
		timestampStr,
		nonce,
	)
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(rawString))
	expectedSign := hex.EncodeToString(mac.Sum(nil))

	// 8. Compare in constant time.
	if !hmac.Equal([]byte(sign), []byte(expectedSign)) {
		slog.WarnContext(ctx, "AK/SK auth: signature mismatch",
			"method", c.Request.Method, "path", c.Request.URL.Path)
		unauthorized(c, "signature verification failed")
		return false
	}

	// 9. Reject a replay: a nonce may be used once within the timestamp skew
	// window. This runs after the signature check so an unauthenticated caller
	// cannot consume nonces belonging to a real caller.
	fresh, err := nonces.Claim(ctx, apiKey, nonce)
	if err != nil {
		slog.ErrorContext(ctx, "AK/SK auth: nonce store unavailable", "error", err)
		unauthorized(c, "nonce replay check unavailable")
		return false
	}
	if !fresh {
		slog.WarnContext(ctx, "AK/SK auth: replayed nonce", "api_key", apiKey)
		unauthorized(c, "nonce has already been used")
		return false
	}

	// The authenticated service becomes the request's authorization
	// subject. The agent flow overwrites it with the end user for the
	// duration of a run; this is what a request that never reaches a run
	// (a resource CRUD, say) is authorized as.
	authedCtx := authz.ContextWithSubject(c.Request.Context(), authz.SubjectFromService(apiKey))
	authedCtx = method.With(authedCtx, method.AKSK)
	c.Request = c.Request.WithContext(authedCtx)

	return true
}

// hasAKSKHeaders reports whether the request carries any AK/SK credential
// header. A partial set is an attempted signed request: it must not fall back
// to another credential type, or a caller could downgrade by dropping a field.
func hasAKSKHeaders(c *gin.Context) bool {
	return c.GetHeader(headerAPIKey) != "" ||
		c.GetHeader(headerTimestamp) != "" ||
		c.GetHeader(headerNonce) != "" ||
		c.GetHeader(headerSign) != ""
}

// normalizeNonces replaces a nil store with a process-local one rather than
// disabling replay detection.
func normalizeNonces(nonces NonceStore) NonceStore {
	if nonces == nil {
		return newMemoryNonceStore()
	}
	return nonces
}

// unauthorized writes the 401 response and aborts the request.
func unauthorized(c *gin.Context, message string) {
	core.WriteResponse(c, nil, errno.ErrUnauthenticated.WithMessage("%s", message))
	c.Abort()
}

// safeProvide calls provider and recovers from any panic, treating it as
// "key not found".
func safeProvide(ctx context.Context, provider SecretProviderFunc, apiKey string) (secretKey string, exists bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "AK/SK auth: secret provider panicked", "api_key", apiKey, "panic", r)
			secretKey, exists = "", false
		}
	}()
	if provider == nil {
		return "", false
	}
	return provider(apiKey)
}
