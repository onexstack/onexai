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

package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	einomcp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/pkg/clientconfig"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	agentv1alpha1 "github.com/onexstack/onexai/pkg/apis/agent/v1alpha1"
	"github.com/onexstack/onexai/pkg/known"
)

// Protocol values accepted in MCPM.Protocol, canonicalised. The constants
// mirror the "SSE" | "HTTP" pair documented on model.MCPM.
const (
	ProtocolHTTP = "HTTP"
	ProtocolSSE  = "SSE"
)

// protocolAliases maps every spelling we accept to its canonical protocol.
// Comparison is case-insensitive. An empty protocol means streamable-HTTP,
// which is what the zke-kubeagent server speaks.
var protocolAliases = map[string]string{
	"":                ProtocolHTTP,
	"http":            ProtocolHTTP,
	"streamable-http": ProtocolHTTP,
	"streamable_http": ProtocolHTTP,
	"streamablehttp":  ProtocolHTTP,
	"streamable":      ProtocolHTTP,
	"sse":             ProtocolSSE,
	"http+sse":        ProtocolSSE,
	"http-sse":        ProtocolSSE,
}

// normalizeProtocol canonicalises a configured protocol value.
//
// Unlike opsassist, which falls back to HTTP for anything unrecognised, an
// unknown value is a hard error here: a typo would otherwise turn into a
// connection failure against the wrong transport, which is far harder to
// diagnose than a "skipping server" warning. Note that "stdio" is deliberately
// unsupported — the agent.mcp table has no command/args/env columns, so a
// stdio server cannot be expressed at all.
func normalizeProtocol(raw string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	p, ok := protocolAliases[key]
	if !ok {
		return "", fmt.Errorf("unsupported protocol %q (want HTTP or SSE)", raw)
	}
	return p, nil
}

// decodeHeaders parses MCPM.Headers, a JSON object of extra HTTP headers.
// An empty value means "no headers". A malformed value is an error rather than
// a silent omission: dropping an auth header yields a puzzling 401 later on.
func decodeHeaders(raw string) (map[string]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(trimmed), &headers); err != nil {
		return nil, fmt.Errorf("decode headers: %w", err)
	}
	return headers, nil
}

// withDefaultPath appends the conventional endpoint path when the configured
// URL carries none, so an operator can store just the host:port.
func withDefaultPath(rawURL, protocol string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("url %q must be absolute (scheme://host)", rawURL)
	}
	if u.Path == "" || u.Path == "/" {
		if protocol == ProtocolSSE {
			u.Path = "/sse"
		} else {
			u.Path = "/mcp"
		}
	}
	return u.String(), nil
}

// connect dials one MCP server and returns its connection and its tools.
//
// The sequence mirrors opsassist's connectMCPServer: build the client, start
// its transport, perform the MCP handshake by hand, then hand the live client
// to einomcp for tool discovery. einomcp does not initialise the session
// itself, so skipping the explicit Initialize makes every ListTools fail.
//
// dialCtx must be the process-lifetime context, not a short-lived one: an SSE
// transport derives its stream context from it and dies along with it. Only the
// handshake and discovery are bounded, by discoveryTimeout.
//
// callerHeader stamps each outgoing MCP request with the identity of the human
// whose turn is running and the agents the run selected.
//
// The human's id lets a tool that scopes its data by owner attribute the work to
// the asker rather than to this service; the agents let a server that scopes its
// tools per agent — the chat server narrows them to the commands the agents were
// granted — answer with the right set. Both are read from the request context,
// so which of them a call carries is decided by what its caller put there.
//
// It is evaluated per request rather than at dial time, which is what makes it
// usable at all: connections are cached by (servers, agents), so a header baked
// into the client at construction would pin every conversation to whichever
// identity happened to open it. Reading it off the request context also keeps it
// out of the connection cache key, and it is applied after the static headers
// decoded from the server row, so a DB-configured Authorization still rides along.
//
// Nothing in context means no header: the tool then sees an unauthenticated
// caller and refuses rather than acting as nobody.
func callerHeader(ctx context.Context) map[string]string {
	headers := make(map[string]string, 2)
	if id := contextx.UserID(ctx); id != "" {
		headers[string(known.XUserID)] = id
	}
	if agents := contextx.AgentNames(ctx); len(agents) > 0 {
		headers[string(known.XAgentNames)] = strings.Join(agents, ",")
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

// agents are the agents this run selected. They ride on the discovery calls —
// the handshake and the tool list — so a server that scopes its tools per agent
// answers with the set those agents were granted; the identity of the human,
// which a tool call needs, is stamped per request by callerHeader instead. The
// set is stated here rather than read per request because discovery happens once
// per (servers, agents) and its result is cached.
//
// The caller owns the returned connection and must Close it.
func connect(dialCtx context.Context, m *model.MCPM, agents []string, discoveryTimeout time.Duration) (mcpConn, []tool.BaseTool, error) {
	protocol, err := normalizeProtocol(m.Protocol)
	if err != nil {
		return nil, nil, err
	}
	headers, err := decodeHeaders(m.Headers)
	if err != nil {
		return nil, nil, err
	}
	endpoint := m.URL
	var httpClient *http.Client
	if m.ClientConfigJSON != "" {
		cfg := &agentv1alpha1.ClientConfig{}
		if err := json.Unmarshal([]byte(m.ClientConfigJSON), cfg); err != nil {
			return nil, nil, fmt.Errorf("decode clientConfig: %w", err)
		}
		resolved, err := clientconfig.Resolve(*cfg)
		if err != nil {
			return nil, nil, err
		}
		endpoint = resolved.URL
		httpClient, err = clientconfig.HTTPClient(*cfg)
		if err != nil {
			return nil, nil, err
		}
		// The row's headers (from headersRef) are the stored secret and win over
		// the inline copy; the inline copy fills a header the secret does not
		// carry.
		if headers == nil {
			headers = make(map[string]string, len(cfg.Headers))
		}
		for key, value := range cfg.Headers {
			if _, ok := headers[key]; !ok {
				headers[key] = value
			}
		}
	}
	endpoint, err = withDefaultPath(endpoint, protocol)
	if err != nil {
		return nil, nil, err
	}

	var cli *client.Client
	if protocol == ProtocolSSE {
		opts := []transport.ClientOption{
			client.WithHeaders(headers),
			client.WithHeaderFunc(callerHeader),
		}
		if httpClient != nil {
			opts = append(opts, client.WithHTTPClient(httpClient))
		}
		cli, err = client.NewSSEMCPClient(endpoint, opts...)
	} else {
		opts := []transport.StreamableHTTPCOption{
			transport.WithHTTPHeaders(headers),
			transport.WithHTTPHeaderFunc(callerHeader),
		}
		if httpClient != nil {
			opts = append(opts, transport.WithHTTPBasicClient(httpClient))
		}
		cli, err = client.NewStreamableHttpClient(endpoint, opts...)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("create client: %w", err)
	}

	if err := cli.Start(dialCtx); err != nil {
		_ = cli.Close()
		return nil, nil, fmt.Errorf("start transport: %w", err)
	}

	// The agent set rides on a context derived from dialCtx: dialCtx itself must
	// stay a plain process-lifetime context (the SSE transport is bound to it),
	// but the discovery calls carry the agents so the server can narrow its tools.
	discoveryCtx := contextx.WithAgentNames(dialCtx, agents)
	initCtx, cancel := context.WithTimeout(discoveryCtx, discoveryTimeout)
	defer cancel()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "oai-agent-apiserver", Version: "1.0.0"}
	if _, err := cli.Initialize(initCtx, initReq); err != nil {
		_ = cli.Close()
		return nil, nil, fmt.Errorf("initialize: %w", err)
	}

	// An empty ToolNameList means "every tool the server exposes"; entries are
	// matched against the wire names, before any sanitising.
	tools, err := einomcp.GetTools(discoveryCtx, &einomcp.Config{Cli: cli, ToolNameList: m.AllowedTools})
	if err != nil {
		_ = cli.Close()
		return nil, nil, fmt.Errorf("list tools: %w", err)
	}

	return cli, tools, nil
}
