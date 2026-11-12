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

// Package webhook is the typed client for run-lifecycle webhook endpoints.
package webhook

import (
	"context"
	"fmt"
	"io"
	"net/http"

	genericoptions "github.com/onexstack/onexstack/pkg/options"

	baseclientset "github.com/onexstack/onexai/internal/pkg/clientset"
)

// maxErrorBody bounds how much of an error response is read.
const maxErrorBody = 64 << 10

// Interface is the typed-client surface.
type Interface interface {
	baseclientset.Interface
	Post(ctx context.Context, url string, body []byte, headers map[string]string) (int, []byte, error)
}

// Client implements Interface.
type Client struct {
	*baseclientset.Client
}

var _ Interface = (*Client)(nil)

// New builds a client. transport is the connection pool shared by every
// delivery; nil keeps resty's default.
//
// No client-level timeout: the deadline comes from the hook's own timeout, per
// call. A client-level timeout would apply to whichever call happened to be
// slowest and could not be attributed to it.
func New(transport http.RoundTripper) *Client {
	base := baseclientset.NewForConfig(&genericoptions.RestyOptions{})
	if transport != nil {
		base.C().SetTransport(transport)
	}
	return &Client{Client: base}
}

// Post sends one JSON body and returns the status and (bounded) response body.
func (c *Client) Post(ctx context.Context, url string, body []byte, headers map[string]string) (int, []byte, error) {
	r := c.R(ctx).SetHeader("Content-Type", "application/json").SetBody(body)
	for key, value := range headers {
		r.SetHeader(key, value)
	}
	resp, err := r.SetResponseDoNotParse(true).Post(url)
	if err != nil {
		return 0, nil, fmt.Errorf("calling %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	detail, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return resp.StatusCode(), nil, fmt.Errorf("reading %s: %w", url, err)
	}
	return resp.StatusCode(), detail, nil
}

// StatusOK reports whether the status is in the 2xx range.
func StatusOK(status int) bool { return status >= 200 && status <= 299 }
