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

// Package a2a is the typed client for a remote A2A runtime: the TLS transport,
// bearer propagation, and Agent Card fetch.
package a2a

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"resty.dev/v3"

	baseclientset "github.com/onexstack/onexai/internal/pkg/clientset"
)

// maxCardBytes bounds an Agent Card.
const maxCardBytes = 1 << 20

// Config describes one remote runtime endpoint.
type Config struct {
	Endpoint           string
	BearerToken        string
	CACert             []byte
	InsecureSkipVerify bool
	// HTTPClient overrides the constructed client. Tests use it to point at an
	// httptest server.
	HTTPClient *http.Client
}

// Interface is the typed-client surface.
type Interface interface {
	baseclientset.Interface
	HTTPClient() *http.Client
	Card(ctx context.Context, endpoint string) (int, []byte, error)
}

// Client implements Interface.
type Client struct {
	*baseclientset.Client
	http *http.Client
}

var _ Interface = (*Client)(nil)

// New builds a transport for one endpoint and wraps it for Card requests.
func New(cfg Config) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = newHTTPClient(cfg)
	}
	base := baseclientset.NewWithResty(resty.New().SetTransport(hc.Transport))
	return &Client{Client: base, http: hc}
}

// HTTPClient exposes the transport to the A2A SDK, which takes an
// *http.Client for both its JSON-RPC and REST transports.
func (c *Client) HTTPClient() *http.Client { return c.http }

// Card fetches the runtime's Agent Card.
func (c *Client) Card(ctx context.Context, endpoint string) (int, []byte, error) {
	url := strings.TrimSuffix(endpoint, "/") + "/.well-known/agent-card.json"
	resp, err := c.R(ctx).SetResponseDoNotParse(true).Get(url)
	if err != nil {
		return 0, nil, fmt.Errorf("fetch card: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCardBytes))
	if err != nil {
		return resp.StatusCode(), nil, fmt.Errorf("read card: %w", err)
	}
	return resp.StatusCode(), data, nil
}

func newHTTPClient(cfg Config) *http.Client {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // explicit local-development switch
	if len(cfg.CACert) > 0 {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(cfg.CACert) {
			tlsConfig.RootCAs = pool
		}
	}
	var round http.RoundTripper = &http.Transport{
		TLSClientConfig:       tlsConfig,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if cfg.BearerToken != "" {
		round = &bearerTransport{base: round, token: cfg.BearerToken}
	}
	return &http.Client{Transport: round}
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(clone)
}
