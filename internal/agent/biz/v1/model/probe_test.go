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

package model

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexai/internal/agent/pkg/llmhttp"
	"github.com/onexstack/onexai/internal/pkg/netguard"
)

func TestSummarizeUpstreamBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "openai envelope",
			body: `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`,
			want: "Incorrect API key provided",
		},
		{
			name: "anthropic envelope",
			body: `{"type":"error","error":{"type":"not_found_error","message":"model: deepseek-flash"}}`,
			want: "model: deepseek-flash",
		},
		{
			name: "string error",
			body: `{"error":"permission denied"}`,
			want: "permission denied",
		},
		{
			name: "top-level message",
			body: `{"message":"quota exceeded"}`,
			want: "quota exceeded",
		},
		{
			name: "plain text",
			body: "Bad Gateway\nfrom proxy",
			want: "Bad Gateway from proxy",
		},
		{
			name: "empty",
			body: "   ",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeUpstreamBody(tt.body); got != tt.want {
				t.Fatalf("summarizeUpstreamBody(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}

	t.Run("truncates long bodies", func(t *testing.T) {
		got := summarizeUpstreamBody(strings.Repeat("a", 1000))
		if len([]rune(got)) != maxProbeMessageRunes+1 {
			t.Fatalf("summary has %d runes, want %d", len([]rune(got)), maxProbeMessageRunes+1)
		}
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("summary does not end with an ellipsis: %q", got)
		}
	})
}

func TestDescribeProbeError(t *testing.T) {
	const apiKey = "sk-secret"

	tests := []struct {
		name    string
		err     error
		want    string
		exclude string
	}{
		{
			name: "upstream 404 keeps the provider message",
			err: &llmhttp.HTTPError{
				Code: http.StatusNotFound,
				Body: `{"error":{"message":"model: deepseek-flash"}}`,
			},
			want: "upstream returned 404: model: deepseek-flash",
		},
		{
			name: "upstream 404 without a body still hints at the URL",
			err:  &llmhttp.HTTPError{Code: http.StatusNotFound},
			want: "upstream returned 404",
		},
		{
			name: "wrapped upstream error",
			err: fmt.Errorf("generate: %w", &llmhttp.HTTPError{
				Code: http.StatusUnauthorized,
				Body: `{"error":{"message":"invalid api key"}}`,
			}),
			want: "upstream returned 401: invalid api key",
		},
		{
			name: "deadline",
			err:  context.DeadlineExceeded,
			want: "connection timed out after 15s",
		},
		{
			name: "client timeout",
			err:  &net.DNSError{Err: "i/o timeout", IsTimeout: true},
			want: "connection timed out after 15s",
		},
		{
			name: "blocked target",
			err:  fmt.Errorf("%w: dial address %q", netguard.ErrBlockedTarget, "127.0.0.1:8080"),
			want: "connection failed: endpoint address is blocked",
		},
		{
			name: "transport error drops the request URL",
			err: &url.Error{
				Op:  "Post",
				URL: "https://api.example.com/v1/chat/completions?api_key=" + apiKey,
				Err: errors.New("dial tcp: connection refused"),
			},
			want:    "connection failed: dial tcp: connection refused",
			exclude: apiKey,
		},
		{
			name: "redacts an echoed key",
			err: &llmhttp.HTTPError{
				Code: http.StatusBadRequest,
				Body: `{"error":{"message":"key ` + apiKey + ` is invalid"}}`,
			},
			want:    "upstream returned 400: key [redacted] is invalid",
			exclude: apiKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeProbeError(tt.err, apiKey, 15*time.Second)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("describeProbeError() = %q, want it to contain %q", got, tt.want)
			}
			if tt.exclude != "" && strings.Contains(got, tt.exclude) {
				t.Fatalf("describeProbeError() = %q, leaked %q", got, tt.exclude)
			}
		})
	}

	t.Run("404 names the base URL as a likely cause", func(t *testing.T) {
		got := describeProbeError(&llmhttp.HTTPError{Code: http.StatusNotFound}, "", 0)
		if !strings.Contains(got, "OpenAI-compatible") {
			t.Fatalf("404 message does not mention the protocol: %q", got)
		}
	})
}

func TestProbeModelReportsUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"model: deepseek-flash"}}`))
	}))
	defer srv.Close()

	resp := probeModel(context.Background(), srv.URL, "test-key", "deepseek-flash", 5*time.Second, true)

	if resp.GetOk() {
		t.Fatal("probe reported success for a 404")
	}
	want := "upstream returned 404: model: deepseek-flash"
	if !strings.Contains(resp.GetMessage(), want) {
		t.Fatalf("message = %q, want it to contain %q", resp.GetMessage(), want)
	}
}

func TestProbeModelSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}],"usage":{"total_tokens":2}}`))
	}))
	defer srv.Close()

	resp := probeModel(context.Background(), srv.URL, "test-key", "deepseek-flash", 5*time.Second, true)

	if !resp.GetOk() {
		t.Fatalf("probe failed: %q", resp.GetMessage())
	}
	if resp.GetMessage() != "ok" {
		t.Fatalf("message = %q, want ok", resp.GetMessage())
	}
}
