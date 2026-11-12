//go:build ignore

// Command onexai-mcp-test exercises onexai's own MCP server
// (started via "onexai mcp") over any of the three transports and
// reports pass/fail for each protocol operation.
//
// The tag keeps it out of ./... : it shares this directory with
// mcp-client.go, which the scaffolder copies into generated projects
// (see the MCP resource types under pkg/apis/agent) and which therefore cannot move.
// Both files are self-contained commands, so building the directory would
// collide on main and textOf. Run this one by path, which ignores the tag:
//
//	go run ./examples/mcpserver/onexcode-mcp-test.go -transport stdio
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func main() {
	transportName := flag.String("transport", "stdio", "MCP transport to use: stdio, sse, or streamable-http.")
	addr := flag.String("addr", ":8090", "Listen address for the sse/streamable-http transports.")
	binPath := flag.String("bin", "onexai", "Path to the onexai binary (stdio transport only).")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := newClient(ctx, *transportName, *binPath, *addr)
	if err != nil {
		log.Fatalf("failed to create %s client: %v", *transportName, err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			log.Printf("warning: close client: %v", err)
		}
	}()

	// ---- MCP protocol handshake (must complete before any other call) ----
	if err := initialize(ctx, c); err != nil {
		log.Fatalf("initialize handshake failed: %v", err)
	}

	// ---- Test suite ----
	var results []testResult

	results = append(results, runTest("tools/list returns expected tools", func() error {
		return testListTools(ctx, c)
	}))

	results = append(results, runTest("tools/call version succeeds", func() error {
		return testCallTool(ctx, c, "version", nil)
	}))

	results = append(results, runTest("tools/call color succeeds", func() error {
		return testCallTool(ctx, c, "color", nil)
	}))

	results = append(results, runTest("tools/call emoji succeeds", func() error {
		return testCallTool(ctx, c, "emoji", nil)
	}))

	results = append(results, runTest("tools/call create shows subcommands", func() error {
		return testCallTool(ctx, c, "create", nil)
	}))

	results = append(results, runTest("tools/call semver shows subcommands", func() error {
		return testCallTool(ctx, c, "semver", nil)
	}))

	results = append(results, runTest("tools/call sysload succeeds", func() error {
		return testCallTool(ctx, c, "sysload", nil)
	}))

	results = append(results, runTest("tools/call unknown tool returns error", func() error {
		_, err := c.CallTool(ctx, mcp.CallToolRequest{
			Params: struct {
				Name      string    `json:"name"`
				Arguments any       `json:"arguments,omitempty"`
				Meta      *mcp.Meta `json:"_meta,omitempty"`
			}{Name: "nonexistent_tool"},
		})
		if err == nil {
			return fmt.Errorf("expected error for unknown tool but got none")
		}
		return nil
	}))

	// ---- Report ----
	var passed, failed int
	for _, r := range results {
		if r.ok {
			fmt.Printf("  %s  %s\n", checkMark(), r.name)
			passed++
		} else {
			fmt.Printf("  %s  %s\n", crossMark(), r.name)
			fmt.Printf("       %s\n", r.message)
			failed++
		}
	}
	fmt.Printf("\n--- Summary: %d passed, %d failed ---\n", passed, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// MCP client factory
// ---------------------------------------------------------------------------

func newClient(ctx context.Context, transportName, binPath, addr string) (*client.Client, error) {
	switch transportName {
	case "stdio":
		return client.NewStdioMCPClient(binPath, nil, "mcp")
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

// initialize performs the required MCP initialize handshake. Without this
// the client refuses any further request with "client not initialized".
func initialize(ctx context.Context, c *client.Client) error {
	_, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: struct {
			ProtocolVersion string                 `json:"protocolVersion"`
			Capabilities    mcp.ClientCapabilities `json:"capabilities"`
			ClientInfo      mcp.Implementation     `json:"clientInfo"`
		}{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo:      mcp.Implementation{Name: "onexai-mcp-test", Version: "1.0.0"},
		},
	})
	return err
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

type testResult struct {
	name    string
	ok      bool
	message string
}

func runTest(name string, fn func() error) testResult {
	if err := fn(); err != nil {
		return testResult{name: name, ok: false, message: err.Error()}
	}
	return testResult{name: name, ok: true}
}

// ---------------------------------------------------------------------------
// Individual test cases
// ---------------------------------------------------------------------------

// testListTools asserts that tools/list returns a non-empty tool list
// containing the expected onexai command tools.
func testListTools(ctx context.Context, c *client.Client) error {
	result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("ListTools failed: %w", err)
	}
	d, _ := json.Marshal(result)
	fmt.Println(string(d))
	if len(result.Tools) == 0 {
		return fmt.Errorf("expected at least one tool")
	}

	// Collect tool names for assertion.
	names := make(map[string]bool)
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}

	// These top-level onexai commands should always be present.
	expected := []string{"version", "color", "emoji", "create", "semver", "sysload"}
	var missing []string
	for _, name := range expected {
		if !names[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing expected tools: %s", strings.Join(missing, ", "))
	}
	return nil
}

// testCallTool calls a tool by name and checks that it succeeds (no transport
// error, no tool-level error, non-empty output).
func testCallTool(ctx context.Context, c *client.Client, name string, args map[string]any) error {
	result, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: struct {
			Name      string    `json:"name"`
			Arguments any       `json:"arguments,omitempty"`
			Meta      *mcp.Meta `json:"_meta,omitempty"`
		}{Name: name, Arguments: args},
	})
	if err != nil {
		return fmt.Errorf("CallTool(%q) transport error: %w", name, err)
	}
	if result.IsError {
		return fmt.Errorf("tool %q returned error: %s", name, textOf(result))
	}
	if textOf(result) == "" {
		return fmt.Errorf("tool %q returned empty output", name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Output helpers
// ---------------------------------------------------------------------------

// textOf extracts the concatenated text of every TextContent block in
// a CallToolResult.
func textOf(result *mcp.CallToolResult) string {
	var out string
	for _, c := range result.Content {
		if text, ok := c.(mcp.TextContent); ok {
			out += text.Text
		}
	}
	return out
}

// checkMark returns a green check mark for terminal output.
func checkMark() string {
	return "\u2705"
}

// crossMark returns a red cross mark for terminal output.
func crossMark() string {
	return "\u274c"
}
