// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// Command mcp-client is a small, standalone MCP client used to exercise
// oai-mcpserver end to end over any of its three transports. It uses
// github.com/mark3labs/mcp-go's client package to speak MCP over stdio, sse,
// or streamable-http, and prints each step so the namespace-grouping
// behavior is easy to observe.
//
// Usage:
//
//	# stdio: mcp-client launches the server binary itself.
//	go run ./examples/mcpserver/mcp-client.go -transport stdio -bin /tmp/oai-mcpserver
//
//	# sse / streamable-http: start oai-mcpserver separately first, e.g.
//	#   /tmp/oai-mcpserver -t sse --addr 127.0.0.1:8081
//	go run ./examples/mcpserver/mcp-client.go -transport sse -addr 127.0.0.1:8081
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// newMCPClient builds and starts an *client.Client for transportName.
// stdio launches binPath as a subprocess (and starts automatically); sse and
// streamable-http instead connect to an oai-mcpserver already listening on
// addr and must be started explicitly.
func newMCPClient(ctx context.Context, transportName, binPath, addr string) (*client.Client, error) {
	switch transportName {
	case "stdio":
		return client.NewStdioMCPClient(binPath, nil)
	case "sse":
		c, err := client.NewSSEMCPClient("http://" + addr + "/sse")
		if err != nil {
			return nil, err
		}
		if err := c.Start(ctx); err != nil {
			return nil, fmt.Errorf("start sse client: %w", err)
		}
		return c, nil
	case "streamable-http":
		c, err := client.NewStreamableHttpClient("http://" + addr + "/mcp")
		if err != nil {
			return nil, err
		}
		if err := c.Start(ctx); err != nil {
			return nil, fmt.Errorf("start streamable-http client: %w", err)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unknown transport %q: must be one of stdio, sse, streamable-http", transportName)
	}
}

// rawCall issues a JSON-RPC request through the client's underlying
// transport, bypassing *client.Client's typed methods. oai-mcpserver exposes
// two extensions beyond the methods the library has typed helpers for: a
// "namespace" filter on tools/list, and the x-namespaces/list method. This
// is the escape hatch both are sent through.
func rawCall(ctx context.Context, c *client.Client, id int64, method string, params any) (json.RawMessage, error) {
	resp, err := c.GetTransport().SendRequest(ctx, transport.JSONRPCRequest{
		JSONRPC: mcp.JSONRPC_VERSION,
		ID:      mcp.NewRequestId(id),
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error.AsError()
	}
	return resp.Result, nil
}

func mustPrettyJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// textOf renders a tools/call result's content blocks as plain text, mirroring
// how oai-mcpserver's handler always returns a single {"type":"text",...} block.
func textOf(result *mcp.CallToolResult) string {
	var out string
	for _, c := range result.Content {
		if text, ok := c.(mcp.TextContent); ok {
			out += text.Text
		}
	}
	return out
}

func main() {
	transportName := flag.String("transport", "stdio", "MCP transport to use: stdio, sse, or streamable-http.")
	binPath := flag.String("bin", "/tmp/oai-mcpserver", "path to the oai-mcpserver binary to launch (stdio transport only)")
	addr := flag.String("addr", "127.0.0.1:8081", "address of an already-running oai-mcpserver (sse/streamable-http transports only)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := newMCPClient(ctx, *transportName, *binPath, *addr)
	if err != nil {
		log.Fatalf("failed to create %s client: %v", *transportName, err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			log.Printf("warning: %v", err)
		}
	}()

	fmt.Println("=== 1. initialize handshake ===")
	initResult, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "mcp-client-example", Version: "1.0.0"},
		},
	})
	must(err)
	fmt.Println(mustPrettyJSON(initResult))

	fmt.Println("\n=== 2. tools/list (all namespaces) ===")
	listAll, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	must(err)
	fmt.Println(mustPrettyJSON(listAll))

	fmt.Println("\n=== 3. tools/list (namespace=echo only) ===")
	listEcho, err := rawCall(ctx, c, 1001, "tools/list", map[string]any{"namespace": "echo"})
	if err != nil {
		fmt.Printf("error: %v\n", err)
	} else {
		fmt.Println(mustPrettyJSON(listEcho))
	}

	fmt.Println("\n=== 4. x-namespaces/list ===")
	namespaces, err := rawCall(ctx, c, 1002, "x-namespaces/list", nil)
	if err != nil {
		fmt.Printf("error: %v\n", err)
	} else {
		fmt.Println(mustPrettyJSON(namespaces))
	}

	fmt.Println("\n=== 5. tools/call echo_ping ===")
	pingResult, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "echo_ping",
			Arguments: map[string]any{"message": "hello from mcp-client"},
		},
	})
	if err != nil {
		fmt.Printf("error: %v\n", err)
	} else {
		fmt.Println(textOf(pingResult))
	}

	fmt.Println("\n=== 6. tools/call system_time.now ===")
	timeResult, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "system_time.now", Arguments: map[string]any{}},
	})
	if err != nil {
		fmt.Printf("error: %v\n", err)
	} else {
		fmt.Println(textOf(timeResult))
	}

	fmt.Println("\n=== 7. tools/call blog_post.search ===")
	searchResult, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "blog_post.search",
			Arguments: map[string]any{"query": "MCP"},
		},
	})
	if err != nil {
		fmt.Printf("error: %v\n", err)
	} else {
		fmt.Println(textOf(searchResult))
	}

	fmt.Println("\n=== 8. tools/call with unknown tool (expect error) ===")
	unknownResult, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "echo_does-not-exist", Arguments: map[string]any{}},
	})
	if err != nil {
		fmt.Printf("got expected error: %s\n", err)
	} else if unknownResult.IsError {
		fmt.Printf("got expected error: %s\n", textOf(unknownResult))
	} else {
		fmt.Println("unexpected: tool call should have failed")
	}

	fmt.Println("\n=== done ===")
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
