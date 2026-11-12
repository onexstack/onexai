// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

package gin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/onexstack/onexai/internal/pkg/errno"
	"github.com/onexstack/onexai/internal/pkg/idempotent"
)

// IdempotencyHeader is the header a caller sets to make a write replay-safe.
//
// It follows the IETF idempotency-key draft rather than onex's X-Idempotent-ID:
// the header name is what a client library already knows, and the value is a
// client-chosen string rather than a server-issued token, because the client is
// the only party that knows two requests are the same one.
const IdempotencyHeader = "Idempotency-Key"

// Idempotent refuses a mutating request whose idempotency key is already
// claimed.
//
// The header is optional: a caller that sends no key gets the pre-existing
// behaviour, and only a caller that asks for replay safety is held to it. That
// is deliberate — requiring a key would break every existing client at once,
// and the endpoints it protects are exactly the ones a careful client would
// want to mark.
//
// A request that fails with a server error releases its claim, so a transient
// failure can be retried with the same key. A 4xx does not: the request shape
// was wrong, and the same key retried would fail the same way.
func Idempotent(store idempotent.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if store == nil || !mutating(c.Request.Method) {
			c.Next()

			return
		}

		key := strings.TrimSpace(c.GetHeader(IdempotencyHeader))
		if key == "" {
			c.Next()

			return
		}

		claimKey := claimScope(c, key)

		fresh, err := store.Claim(c.Request.Context(), claimKey)
		if err != nil {
			// Refusing is the safe answer: the alternative is executing a write
			// whose replay protection did not work.
			core.WriteResponse(c, nil, errno.ErrIdempotencyStoreUnavailable)
			c.Abort()

			return
		}

		if !fresh {
			core.WriteResponse(c, nil, errno.ErrIdempotencyConflict)
			c.Abort()

			return
		}

		c.Next()

		if c.Writer.Status() >= http.StatusInternalServerError {
			// The request context is already canceled by the time a failed
			// handler returns; the release still has to happen.
			_ = store.Release(context.WithoutCancel(c.Request.Context()), claimKey)
		}
	}
}

// mutating reports whether a method can change server state.
func mutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// claimScope turns the caller's key into the string that is stored.
//
// The endpoint is part of the scope because a client that reuses one key across
// two endpoints is making two different requests, and the second must not be
// refused on behalf of the first. The value is hashed so a key cannot be read
// out of the store's key names.
func claimScope(c *gin.Context, key string) string {
	sum := sha256.Sum256([]byte(c.Request.Method + " " + c.FullPath() + "\x00" + key))

	return hex.EncodeToString(sum[:])
}
