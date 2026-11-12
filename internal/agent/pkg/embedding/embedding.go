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

// Package embedding calls an OpenAI-compatible /embeddings endpoint.
package embedding

import (
	"context"
	"fmt"

	typed "github.com/onexstack/onexai/internal/agent/pkg/clientset/typed/embedding"
)

// Client is the embeddings caller. The HTTP transport lives in the typed
// client under pkg/clientset; this type keeps the domain conversion.
type Client struct {
	client *typed.Client
}

// New returns a client with a bounded timeout.
func New() *Client {
	return &Client{client: typed.New()}
}

// Embed returns one vector per input text.
func (c *Client) Embed(ctx context.Context, baseURL, apiKey, modelName string, texts []string) ([][]float32, error) {
	if baseURL == "" || modelName == "" {
		return nil, fmt.Errorf("embedding: endpoint and model are required")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	resp, err := c.client.Embed(ctx, baseURL, apiKey, typed.Request{Model: modelName, Input: texts})
	if err != nil {
		return nil, err
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("embedding: asked for %d vectors, got %d", len(texts), len(resp.Data))
	}
	out := make([][]float32, len(resp.Data))
	for i, item := range resp.Data {
		vector := make([]float32, len(item.Embedding))
		for j, value := range item.Embedding {
			vector[j] = float32(value)
		}
		out[i] = vector
	}
	return out, nil
}
