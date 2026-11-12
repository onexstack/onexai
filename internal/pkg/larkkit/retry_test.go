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

package larkkit

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
)

// TestIsTransientError pins the classification the retry loop turns on.
//
// The distinction is not cosmetic. The loop gives up on the first error it does
// not consider transient, so one misclassification in either direction is a
// failure mode: calling a permission refusal transient turns a revoked grant into
// a retry storm, and calling a rate limit permanent makes a throttled pass fail
// where backing off would have carried it.
func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not an error to retry", nil, false},

		// Network-level failures: the request did not reach a decision.
		{"a reset connection", fmt.Errorf("post: %w", errors.New("connection reset by peer")), true},
		{"an unexpected EOF", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), true},
		{"a timeout", netError{timeout: true}, true},
		{"a DNS failure", errors.New("lookup open.feishu.cn: server misbehaving"), true},

		// Answer-embedded failures, classified by the HTTP status.
		{"a rate limit answered with 429", NewAPIError("batch update records", 0, http.StatusTooManyRequests, "too many requests"), true},
		{"a server error", NewAPIError("batch update records", 0, http.StatusInternalServerError, "internal error"), true},
		{"a bad gateway", NewAPIError("batch update records", 0, http.StatusBadGateway, "bad gateway"), true},
		{"a service unavailable", NewAPIError("batch update records", 0, http.StatusServiceUnavailable, "unavailable"), true},
		{"a gateway timeout", NewAPIError("batch update records", 0, http.StatusGatewayTimeout, "timeout"), true},

		// The platform's own rate-limit code, which arrives with HTTP 200 and so
		// can only be identified by the business code.
		{"the platform's frequency limit", NewAPIError("batch create records", CodeFrequencyLimit, http.StatusOK, "frequency limit"), true},

		// Refusals: another attempt would produce the same answer.
		{"a permission refusal", NewAPIError("batch update records", 0, http.StatusForbidden, "forbidden"), false},
		{"a bad request", NewAPIError("batch update records", 0, http.StatusBadRequest, "bad request"), false},
		{"a not-found answer", NewAPIError("batch get records", 0, http.StatusNotFound, "not found"), false},
		{"an invalid field value", NewAPIError("batch create records", 1254005, http.StatusOK, "field value is invalid"), false},
		{"an unrecognised business code", NewAPIError("batch update records", 99999999, http.StatusOK, "something"), false},
		{"a plain error", errors.New("the record no longer exists"), false},

		// Wrapping must not hide the classification: every call site wraps the
		// typed error with the operation it was performing.
		{"a wrapped rate limit", fmt.Errorf("update 100 records in orders: %w", NewAPIError("batch update records", 0, http.StatusTooManyRequests, "too many requests")), true},
		{"a wrapped permission refusal", fmt.Errorf("update 100 records in orders: %w", NewAPIError("batch update records", 0, http.StatusForbidden, "forbidden")), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTransientError(tt.err); got != tt.want {
				t.Errorf("IsTransientError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestAPIErrorRendersBothCodes pins the message shape, because it is what the
// operation log carries when a Bitable call is refused: the operation, the
// platform's code, the status when the SDK surfaced one, and the platform's
// message.
func TestAPIErrorRendersBothCodes(t *testing.T) {
	withStatus := NewAPIError("batch update records", 1254005, http.StatusOK, "field value is invalid")
	if got, want := withStatus.Error(), "batch update records failed: code=1254005 status=200 msg=field value is invalid"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	withoutStatus := NewAPIError("fetch tenant access token", 99991663, 0, "invalid token")
	if got, want := withoutStatus.Error(), "fetch tenant access token failed: code=99991663 msg=invalid token"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestTenantTokenFailureIsRecognised pins that the token layer acts on the typed
// code, not only on the SDK's message text.
func TestTenantTokenFailureIsRecognised(t *testing.T) {
	typed := fmt.Errorf("call: %w", NewAPIError("list bitable records", CodeTenantTokenInvalid, http.StatusOK, "invalid access token"))
	if !isTenantAccessTokenInvalid(typed) {
		t.Error("a typed invalid-token error was not recognised")
	}
	// The string fallback stays for errors the SDK builds itself.
	if !isTenantAccessTokenInvalid(errors.New("code: 99991663, msg: invalid access token")) {
		t.Error("the SDK's own invalid-token error was not recognised")
	}
	if isTenantAccessTokenInvalid(NewAPIError("list bitable records", 1254005, http.StatusOK, "field value is invalid")) {
		t.Error("an unrelated refusal was read as an expired token")
	}
}

// netError is a net.Error with a fixed timeout flag.
type netError struct{ timeout bool }

func (e netError) Error() string   { return "network error" }
func (e netError) Timeout() bool   { return e.timeout }
func (e netError) Temporary() bool { return e.timeout }
