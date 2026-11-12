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
// The original repo for this file is https://github.com/onexstack/onex.

// Package k8sgpt turns a fleet of k8sgpt MCP servers — one per Kubernetes
// cluster — into a single set of tools.
//
// The fleet is the reason this package exists rather than reusing the general
// MCP client: there is one server per cluster, the tools are identical across
// them, and the model should be choosing a *cluster* rather than choosing
// between twenty copies of the same tool. So the tool list is taken from one
// server (the primary), and every tool gains two routing arguments the router
// strips before forwarding: which cluster, and which account it lives in.
package k8sgpt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/onexstack/onexai/internal/agent/model"
)

// slowCallThreshold is how long one forwarded call may take before it is worth
// a log line. The calls run cluster diagnostics, so they are slow by nature;
// the threshold marks the ones that are slow enough to explain a stalled turn.
const slowCallThreshold = 30 * time.Second

// clusterKey identifies one MCP server in the fleet: a cluster belongs to an
// account, and the same cluster name can exist in two.
func clusterKey(accountID, clusterName string) string {
	return accountID + "/" + clusterName
}

// Router holds one MCP client per cluster and forwards tool calls to it.
//
// The clients it dials are not closed: there is no reload path that would
// orphan them, and process exit reclaims the sockets. A router that gains a
// reload would need a Close and a caller for it.
type Router struct {
	endpoints []*model.K8SGPTEndpointM
	// token is the bearer credential every endpoint is called with. Empty sends
	// no Authorization header.
	token string

	mu      sync.RWMutex
	clients map[string]*client.Client
	// connectErr records, per cluster, why it is not usable, so a call that
	// names it can say so instead of reporting an unknown cluster.
	connectErr map[string]error
}

// NewRouter returns a Router over the given endpoints.
func NewRouter(endpoints []*model.K8SGPTEndpointM, token string) *Router {
	return &Router{
		endpoints:  endpoints,
		token:      token,
		clients:    make(map[string]*client.Client),
		connectErr: make(map[string]error),
	}
}

// Connect dials every endpoint, concurrently, and records what failed.
//
// It returns the clusters that came up. A cluster that does not answer is
// reported and skipped rather than failing the whole router: a fleet where one
// server is down should still answer about the other nineteen.
func (r *Router) Connect(ctx context.Context) []string {
	if len(r.endpoints) == 0 {
		return nil
	}

	var (
		wg     sync.WaitGroup
		usable []string
	)
	for _, endpoint := range r.endpoints {
		if endpoint == nil || endpoint.Endpoint == "" {
			continue
		}
		wg.Go(func() {
			cli, err := r.dial(ctx, endpoint)
			// The same lock the readers take. A local one would leave the maps
			// open to a concurrent read — luck rather than design, since
			// nothing today calls a tool during startup.
			r.mu.Lock()
			defer r.mu.Unlock()
			if err != nil {
				slog.WarnContext(ctx, "[k8sgpt] cluster unavailable",
					"cluster", endpoint.ClusterName, "account", endpoint.AccountID, "error", err)
				r.connectErr[clusterKey(endpoint.AccountID, endpoint.ClusterName)] = err
				return
			}
			r.clients[clusterKey(endpoint.AccountID, endpoint.ClusterName)] = cli
			usable = append(usable, endpoint.ClusterName)
		})
	}
	wg.Wait()
	return usable
}

// dial connects and initializes one endpoint.
func (r *Router) dial(ctx context.Context, endpoint *model.K8SGPTEndpointM) (*client.Client, error) {
	url := endpoint.Endpoint
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}

	headers := map[string]string{}
	if r.token != "" {
		headers["Authorization"] = "Bearer " + r.token
	}

	var (
		cli *client.Client
		err error
	)
	if isStreamableHTTP(endpoint.Protocol) {
		cli, err = client.NewStreamableHttpClient(url, transport.WithHTTPHeaders(headers))
	} else {
		cli, err = client.NewSSEMCPClient(url, transport.WithHeaders(headers))
	}
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}
	if err := cli.Start(ctx); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "onexai-k8sgpt-router", Version: "1.0.0"}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return cli, nil
}

// isStreamableHTTP reports whether a configured protocol names streamable HTTP.
// Anything else is treated as SSE, which is the older of the two and the one a
// record that predates the field used.
func isStreamableHTTP(protocol string) bool {
	p := strings.ToLower(strings.TrimSpace(protocol))
	return p == "streamablehttp" || p == "streamable-http" || p == "streamable_http" || p == "http"
}

// primaryClient returns the client to take the tool list from: the endpoint
// marked primary, or any connected one when none is.
func (r *Router) primaryClient() (*client.Client, string) {
	for _, endpoint := range r.endpoints {
		if endpoint == nil || !strings.EqualFold(strings.TrimSpace(endpoint.IsPrimaryMCP), "yes") {
			continue
		}
		if cli, ok := r.clients[clusterKey(endpoint.AccountID, endpoint.ClusterName)]; ok {
			return cli, endpoint.ClusterName
		}
	}
	for key, cli := range r.clients {
		return cli, key
	}
	return nil, ""
}

// Clusters lists the connected cluster names, sorted by the order the endpoints
// declared them so the tool schema a model sees is stable across restarts.
func (r *Router) Clusters() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	var out []string
	for _, endpoint := range r.endpoints {
		if endpoint == nil || seen[endpoint.ClusterName] {
			continue
		}
		if _, ok := r.clients[clusterKey(endpoint.AccountID, endpoint.ClusterName)]; !ok {
			continue
		}
		seen[endpoint.ClusterName] = true
		out = append(out, endpoint.ClusterName)
	}
	return out
}

// Accounts lists the connected account ids, in endpoint order.
func (r *Router) Accounts() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	var out []string
	for _, endpoint := range r.endpoints {
		if endpoint == nil || seen[endpoint.AccountID] {
			continue
		}
		if _, ok := r.clients[clusterKey(endpoint.AccountID, endpoint.ClusterName)]; !ok {
			continue
		}
		seen[endpoint.AccountID] = true
		out = append(out, endpoint.AccountID)
	}
	return out
}

// CallTool forwards one call to the cluster's server and returns its text.
//
// It is exported so a plain HTTP handler can reuse the same routing the tools
// use, rather than growing a second idea of which cluster a request means.
func (r *Router) CallTool(ctx context.Context, accountID, clusterName, toolName string, args map[string]any) (string, error) {
	r.mu.RLock()
	cli, ok := r.clients[clusterKey(accountID, clusterName)]
	connectErr := r.connectErr[clusterKey(accountID, clusterName)]
	r.mu.RUnlock()

	if !ok {
		if connectErr != nil {
			return "", fmt.Errorf("cluster %s (account %s) is not available: %w", clusterName, accountID, connectErr)
		}
		return "", fmt.Errorf("no k8sgpt server is registered for cluster %s in account %s", clusterName, accountID)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args

	start := time.Now()
	result, err := cli.CallTool(ctx, req)
	elapsed := time.Since(start)
	if err != nil {
		return "", fmt.Errorf("k8sgpt %s on %s: %w", toolName, clusterName, err)
	}
	if result.IsError {
		return "", fmt.Errorf("k8sgpt %s on %s returned an error: %s", toolName, clusterName, renderContent(result.Content))
	}
	if elapsed > slowCallThreshold {
		slog.WarnContext(ctx, "[k8sgpt] slow call", "tool", toolName, "cluster", clusterName,
			"elapsed_ms", elapsed.Milliseconds())
	}
	return renderContent(result.Content), nil
}

// renderContent flattens an MCP result into text.
func renderContent(contents []mcp.Content) string {
	var sb strings.Builder
	for _, content := range contents {
		if text, ok := content.(mcp.TextContent); ok {
			sb.WriteString(text.Text)
			sb.WriteString("\n")
			continue
		}
		if raw, err := json.Marshal(content); err == nil {
			sb.Write(raw)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
