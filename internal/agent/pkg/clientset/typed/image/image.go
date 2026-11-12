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

// Package image is the typed client for an OpenAI-compatible
// /images/generations endpoint.
package image

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"

	baseclientset "github.com/onexstack/onexai/internal/pkg/clientset"
)

// maxBody bounds a provider's answer so a stream cannot make this process read
// without limit. Ten images at the largest sizes a provider offers stays well
// inside it.
const maxBody = 64 << 20

// Request is one image-generation call.
type Request struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	ResponseFormat string `json:"response_format,omitempty"`
	Size           string `json:"size,omitempty"`
	Quality        string `json:"quality,omitempty"`
}

// Interface is the typed-client surface.
type Interface interface {
	baseclientset.Interface
	// Generate posts one request and returns the raw body and status.
	Generate(ctx context.Context, req Request) ([]byte, int, error)
}

// Client implements Interface.
type Client struct {
	*baseclientset.Client
}

var _ Interface = (*Client)(nil)

// NewForConfig builds a client for baseURL.
//
// The retry count is left at zero: drawing is a paid request the provider has
// already been asked to perform, and a retry after a timeout — the case where
// the answer is least certain — would bill for a second drawing to find out
// whether the first produced one.
func NewForConfig(baseURL, apiKey string) *Client {
	opts := &genericoptions.RestyOptions{
		Endpoint: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:    apiKey,
		// Drawing is slower than answering, and a provider that queues can take
		// a minute. The timeout is the caller's patience, not a health check.
		Timeout: 3 * time.Minute,
	}
	return &Client{Client: baseclientset.NewForConfig(opts)}
}

// Generate posts one request. The body is returned raw so the caller keeps the
// provider's own message when the status is not 200.
func (c *Client) Generate(ctx context.Context, req Request) ([]byte, int, error) {
	if req.N <= 0 {
		req.N = 1
	}
	resp, err := c.R(ctx).SetResponseDoNotParse(true).SetBody(req).Post("/images/generations")
	if err != nil {
		return nil, 0, fmt.Errorf("image generation: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, resp.StatusCode(), fmt.Errorf("image generation: read response: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		// The provider's own message is the only thing that says whether this
		// was a bad prompt, a missing model, or a quota.
		return payload, resp.StatusCode(), fmt.Errorf("image generation returned HTTP %d: %s",
			resp.StatusCode(), baseclientset.Truncate(payload, 512))
	}
	return payload, resp.StatusCode(), nil
}
