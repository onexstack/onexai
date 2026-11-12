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

// Package kb is the typed client shared by the knowledge-base retrievers
// (Dify, RAGFlow, and the generic platform adapter).
package kb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"

	baseclientset "github.com/onexstack/onexai/internal/pkg/clientset"
)

// maxResponseBytes bounds one provider answer.
const maxResponseBytes = 16 << 20

// Interface is the typed-client surface.
type Interface interface {
	baseclientset.Interface
	Post(ctx context.Context, url string, headers map[string]string, body any) (int, []byte, error)
}

// Client implements Interface.
type Client struct {
	*baseclientset.Client
}

var _ Interface = (*Client)(nil)

// NewForConfig builds a client with the provider timeout.
func NewForConfig(timeout time.Duration) *Client {
	opts := &genericoptions.RestyOptions{Timeout: timeout}
	return &Client{Client: baseclientset.NewForConfig(opts)}
}

// NewWithHTTPClient builds a client over a caller-supplied transport. It keeps
// the old escape hatch for tests and proxies.
func NewWithHTTPClient(c *http.Client) *Client {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	opts := &genericoptions.RestyOptions{Timeout: timeout}
	base := baseclientset.NewForConfig(opts)
	base.C().SetTransport(c.Transport)
	return &Client{Client: base}
}

// Post sends one JSON body and returns the raw status and body. Decoding stays
// with the provider, which owns the response shape and its error wording.
func (c *Client) Post(ctx context.Context, url string, headers map[string]string, body any) (int, []byte, error) {
	r := c.R(ctx).SetBody(body)
	for key, value := range headers {
		r.SetHeader(key, value)
	}
	resp, err := r.SetResponseDoNotParse(true).Post(url)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode(), nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode(), data, nil
}
