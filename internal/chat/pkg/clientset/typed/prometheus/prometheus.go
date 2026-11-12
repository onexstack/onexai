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

// Package prometheus provides a typed client for the Prometheus HTTP API's
// instant-query endpoint (/api/v1/query), used by the /pod ChatOps command to
// read kube-state-metrics.
//
// A Prometheus here is expected to be reachable anonymously: there is no
// credential to configure and none is sent. When no endpoint is configured the
// constructor returns a no-op client that fails every call with a readable
// error, mirroring the Zadig client, so a caller never has to nil-check.
package prometheus

import (
	"context"
	"fmt"
	"strings"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// Interface defines the Prometheus client operations.
type Interface interface {
	// Query runs an instant PromQL query and returns the resulting vector. A
	// scalar, matrix or string result is an error: every query this service
	// issues is a vector.
	Query(ctx context.Context, query string) (model.Vector, error)
	// Configured reports whether an endpoint was configured at all. It exists so
	// a command can refuse with "未配置 Prometheus 指标地址" (Prometheus metrics address not configured) before running a
	// query that could only fail.
	Configured() bool
}

// Client implements Interface against a Prometheus HTTP API.
type Client struct {
	api promv1.API
}

var _ Interface = (*Client)(nil)

// noopClient is returned when no endpoint is configured or the client cannot be
// built.
type noopClient struct {
	err error
}

func (n *noopClient) Query(_ context.Context, _ string) (model.Vector, error) {
	return nil, n.err
}

func (n *noopClient) Configured() bool { return false }

// NewClient creates a Prometheus client for the given HTTP API base URL (for
// example "http://prometheus:9090"). When endpoint is empty, or the address
// cannot be parsed, a no-op client is returned that fails every call with a
// clear error rather than a nil dereference.
func NewClient(endpoint string) Interface {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return &noopClient{err: fmt.Errorf("prometheus: endpoint not configured")}
	}

	client, err := promapi.NewClient(promapi.Config{Address: endpoint})
	if err != nil {
		return &noopClient{err: fmt.Errorf("prometheus: build client for %q: %w", endpoint, err)}
	}

	return &Client{api: promv1.NewAPI(client)}
}

// Configured reports whether an endpoint was configured.
func (c *Client) Configured() bool { return true }

// Query runs an instant PromQL query.
func (c *Client) Query(ctx context.Context, query string) (model.Vector, error) {
	// Warnings are informational (typically "metric does not exist"); the
	// caller decides whether the empty vector that follows is a problem.
	result, _, err := c.api.Query(ctx, query, time.Now())
	if err != nil {
		return nil, fmt.Errorf("prometheus: query %q: %w", query, err)
	}

	vector, ok := result.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("prometheus: query %q: unexpected result type %T", query, result)
	}
	return vector, nil
}
