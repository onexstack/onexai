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

// Package skill is the typed client for the skill-source endpoints (GitHub,
// GitLab, LarkDoc).
package skill

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"

	baseclientset "github.com/onexstack/onexai/internal/pkg/clientset"
)

// Interface is the typed-client surface.
type Interface interface {
	baseclientset.Interface
	// Get downloads one URL with the given headers, refusing a body over max.
	Get(ctx context.Context, url string, headers map[string]string, max int64) (int, []byte, error)
	// HTTPClient exposes the underlying *http.Client for SDKs that take one.
	HTTPClient() *http.Client
}

// Client implements Interface.
type Client struct {
	*baseclientset.Client
}

var _ Interface = (*Client)(nil)

// New builds a client with the download timeout.
func New(timeout time.Duration) *Client {
	return &Client{Client: baseclientset.NewForConfig(&genericoptions.RestyOptions{Timeout: timeout})}
}

// NewWithHTTPClient builds a client over a caller-supplied transport.
func NewWithHTTPClient(c *http.Client) *Client {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	base := baseclientset.NewForConfig(&genericoptions.RestyOptions{Timeout: timeout})
	base.C().SetTransport(c.Transport)
	return &Client{Client: base}
}

// HTTPClient exposes the underlying *http.Client for SDKs (the GitLab client
// takes one).
func (c *Client) HTTPClient() *http.Client { return c.C().Client() }

// Get downloads one URL. A body over max is refused rather than truncated: a
// truncated archive fails to open with a confusing error, and one that happens
// to open would silently contain half a skill.
func (c *Client) Get(ctx context.Context, url string, headers map[string]string, max int64) (int, []byte, error) {
	r := c.R(ctx)
	for key, value := range headers {
		r.SetHeader(key, value)
	}
	resp, err := r.SetResponseDoNotParse(true).Get(url)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	data, err := readBounded(resp.Body, max)
	if err != nil {
		return resp.StatusCode(), nil, err
	}
	return resp.StatusCode(), data, nil
}

// readBounded reads a response body, refusing anything over the cap.
func readBounded(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("response exceeds the %d-byte limit", max)
	}
	return data, nil
}
