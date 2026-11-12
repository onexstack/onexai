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

// Package clientset is the shared base for the typed third-party HTTP clients
// each service keeps under internal/<service>/pkg/clientset.
//
// The shape follows the typed clients in onexdemo: an Interface exposing the
// underlying resty client (C), a per-request builder (R), and a Healthz check;
// construction from onexstack's RestyOptions so authentication, timeout,
// retries and TLS are configured in one place.
package clientset

import (
	"context"
	"fmt"
	"net/http"
	"unicode/utf8"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"resty.dev/v3"
)

// Interface is the smallest contract a typed client exposes.
type Interface interface {
	// C returns the underlying resty client.
	C() *resty.Client
	// R creates a request bound to ctx.
	R(ctx context.Context) *resty.Request
	// Healthz checks that the configured endpoint answers /healthz.
	Healthz(ctx context.Context) error
}

// Client implements Interface over a resty client.
type Client struct {
	client *resty.Client
}

var _ Interface = (*Client)(nil)

// NewForConfig builds a client from onexstack's RestyOptions. WithTrace is
// applied here so every typed client propagates trace headers without repeating
// the call.
func NewForConfig(opts *genericoptions.RestyOptions) *Client {
	return &Client{client: opts.WithTrace().NewClient()}
}

// NewWithResty wraps an already-configured resty client. It exists for the CMDB
// client, whose HMAC signing middleware predates this base.
func NewWithResty(rc *resty.Client) *Client { return &Client{client: rc} }

// C returns the underlying resty client.
func (c *Client) C() *resty.Client { return c.client }

// R creates a request bound to ctx. The context is set per request rather than
// through Client.SetContext: the latter mutates the shared client and races
// between concurrent requests.
func (c *Client) R(ctx context.Context) *resty.Request {
	return c.client.R().SetContext(ctx)
}

// Healthz performs a health check against the configured endpoint.
func (c *Client) Healthz(ctx context.Context) error {
	resp, err := c.R(ctx).Get("/healthz")
	if err != nil {
		return fmt.Errorf("health check request: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("health check: status %d: %s", resp.StatusCode(), Truncate(resp.Bytes(), 512))
	}
	return nil
}

// Truncate returns at most max bytes of raw, appending "..." when it had to
// cut. It is the byte-oriented form used for log and error previews.
func Truncate(raw []byte, max int) string {
	if len(raw) <= max {
		return string(raw)
	}
	return string(raw[:max]) + "..."
}

// TruncateRunes keeps the tail off at a UTF-8 boundary. A byte slice that ends
// mid-rune is invalid UTF-8, and a chat card that receives one may reject the
// whole message rather than show a shortened one.
func TruncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	// Back off to a rune boundary. Only the last rune can be partial, and
	// DecodeLastRuneInString finds it in constant time.
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut + "\n… (truncated)"
}

// ErrorFromResponse turns a non-2xx response into an error carrying the status
// and a bounded body preview.
func ErrorFromResponse(resp *resty.Response) error {
	if resp == nil {
		return fmt.Errorf("empty response")
	}
	return fmt.Errorf("status %d: %s", resp.StatusCode(), Truncate(resp.Bytes(), 512))
}
