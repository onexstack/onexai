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

// Package usage is the typed client for the platform's usage-report endpoint.
package usage

import (
	"context"
	"fmt"
	"io"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"

	baseclientset "github.com/onexstack/onexai/internal/pkg/clientset"
)

// maxResponseBytes bounds one answer.
const maxResponseBytes = 4 << 10

// Interface is the typed-client surface.
type Interface interface {
	baseclientset.Interface
	Post(ctx context.Context, path string, body []byte) (int, []byte, error)
}

// Client implements Interface.
type Client struct {
	*baseclientset.Client
}

var _ Interface = (*Client)(nil)

// New builds a client for the given endpoint and token.
func New(endpoint, token string, timeout time.Duration) *Client {
	opts := &genericoptions.RestyOptions{
		Endpoint: endpoint,
		Token:    token,
		Timeout:  timeout,
	}
	return &Client{Client: baseclientset.NewForConfig(opts)}
}

// Post sends one JSON body and returns the status and (bounded) response body.
func (c *Client) Post(ctx context.Context, path string, body []byte) (int, []byte, error) {
	resp, err := c.R(ctx).SetHeader("Content-Type", "application/json").SetBody(body).Post(path)
	if err != nil {
		return 0, nil, fmt.Errorf("calling %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode(), nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return resp.StatusCode(), data, nil
}
