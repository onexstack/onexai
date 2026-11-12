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

// Package embedding is the typed client for an OpenAI-compatible /embeddings
// endpoint.
package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"

	baseclientset "github.com/onexstack/onexai/internal/pkg/clientset"
)

// Request is one embeddings call.
type Request struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// Response is the provider's answer.
type Response struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// Interface is the typed-client surface.
type Interface interface {
	baseclientset.Interface
	Embed(ctx context.Context, baseURL, apiKey string, req Request) (*Response, error)
}

// Client implements Interface. It is built once and reused: the endpoint and
// credential vary per call (a run may name its own embedding model), but the
// connection pool is shared.
type Client struct {
	*baseclientset.Client
}

var _ Interface = (*Client)(nil)

// New builds a client with a bounded timeout.
func New() *Client {
	opts := &genericoptions.RestyOptions{Timeout: 30 * time.Second}
	return &Client{Client: baseclientset.NewForConfig(opts)}
}

// Embed posts one request to the given endpoint.
func (c *Client) Embed(ctx context.Context, baseURL, apiKey string, req Request) (*Response, error) {
	reqURL := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/embeddings"
	r := c.R(ctx).SetBody(req)
	if apiKey != "" {
		r.SetAuthToken(apiKey)
	}
	resp, err := r.Post(reqURL)
	if err != nil {
		return nil, fmt.Errorf("embedding: request: %w", err)
	}
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		return nil, fmt.Errorf("embedding: status %d: %s",
			resp.StatusCode(), baseclientset.Truncate(resp.Bytes(), 4<<10))
	}
	// Decoded explicitly rather than through SetResult: a compatible provider
	// may omit the JSON content type, which the original caller ignored too.
	var out Response
	if err := json.Unmarshal(resp.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("embedding: decode response: %w", err)
	}
	return &out, nil
}
