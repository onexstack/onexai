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
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
)

func TestIsTenantAccessTokenInvalid(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"code 99991663", fmt.Errorf("api error code=99991663"), true},
		{"invalid access token", errors.New("invalid access token"), true},
		{"case insensitive", errors.New("Invalid Access Token"), true},
		{"unrelated", errors.New("rate limited"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTenantAccessTokenInvalid(tt.err); got != tt.want {
				t.Errorf("isTenantAccessTokenInvalid(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"conn reset typed", syscall.ECONNRESET, true},
		{"broken pipe typed", syscall.EPIPE, true},
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"net timeout", timeoutError{}, true},
		{"connection refused string", errors.New("dial tcp: connection refused"), true},
		{"non-transient", errors.New("invalid request"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientError(tt.err); got != tt.want {
				t.Errorf("isTransientError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// timeoutError is a net.Error whose Timeout() reports true.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}
