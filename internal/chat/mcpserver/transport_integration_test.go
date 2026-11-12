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

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/feizaonet/cobrax"
	mcpclient "github.com/mark3labs/mcp-go/client"
	mcphttp "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
	"github.com/onexstack/onexai/pkg/options"
)

// whoamiCmd builds a one-command tree whose command reports the identity it sees
// on its context. Reading it inside RunE is the assertion that matters: the
// value has to survive the transport middleware, mcp-go's dispatch, cobrax's
// middleware and its in-process execution, all of which are between the header
// arriving and the command running.
func whoamiCmd(out runtime.Responder) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "report the caller identity this command can see",
		Annotations: map[string]string{
			chat.MCPTool: chat.True,
		},
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return out.SendText(cmd.Context(), "identity="+runtime.From(cmd.Context()).Identity())
		},
	}
}

// roleCmd reports the roles this command can see, and declares itself SRE-only
// so that the gate — not this command — decides whether it runs.
func roleCmd(out runtime.Responder) *cobra.Command {
	return &cobra.Command{
		Use:   "whoroles",
		Short: "report the caller's roles as the gate sees them",
		Annotations: map[string]string{
			chat.MCPTool: chat.True,
			chat.SREOnly: chat.True,
		},
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dr := runtime.From(cmd.Context())
			return out.SendText(cmd.Context(), fmt.Sprintf("sre=%t admin=%t", dr.IsSRE, dr.IsAdmin))
		},
	}
}

// serveWhoami starts the real transport on a loopback port, serving a tree that
// holds whoami alone, and returns its base URL plus a stop function.
func serveWhoami(t *testing.T) (string, func()) {
	t.Helper()

	return serveTree(t, roleRuntime(), nil, func(out runtime.Responder) []*cobra.Command {
		return []*cobra.Command{whoamiCmd(out)}
	})
}

// serveTree starts the real transport on a loopback port over the tree commands
// builds, and returns its base URL plus a stop function.
//
// preRun, when given, is what the real tree installs at its root — the role gate
// is the one a test needs, because a tool refused for its role and a tool that
// ran are the same shape at the protocol level when nothing checks.
func serveTree(
	t *testing.T,
	sr *runtime.StaticRuntime,
	preRun func(*cobra.Command, []string) error,
	commands func(runtime.Responder) []*cobra.Command,
) (string, func()) {
	t.Helper()

	addr := freeAddr(t)

	factory := func() *cobra.Command {
		var root *cobra.Command
		out := newResponder(func() *cobra.Command { return root })
		root = &cobra.Command{
			Use:               "/",
			SilenceErrors:     true,
			SilenceUsage:      true,
			PersistentPreRunE: preRun,
		}
		root.AddCommand(commands(out)...)
		return root
	}

	srv, err := cobrax.NewMCPServer(
		cobrax.MCPOptions{Enabled: true, Addr: addr, Name: "oai-chat-apiserver", Version: "test"},
		factory,
		cobrax.WithSelectors(selector()),
		cobrax.WithTransport(newTransport(&options.MCPOptions{Addr: addr}, sr)),
	)
	if err != nil {
		t.Fatalf("NewMCPServer returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Start(ctx) }()

	base := "http://" + addr
	waitForListener(t, addr)

	return base, func() {
		cancel()
		select {
		case <-served:
		case <-time.After(15 * time.Second):
			t.Error("the MCP listener did not shut down after its context was cancelled")
		}
	}
}

// freeAddr asks the kernel for an unused port, then releases it.
func freeAddr(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not reserve a port: %v", err)
	}
	defer l.Close() //nolint:errcheck

	return l.Addr().String()
}

// waitForListener blocks until addr accepts a connection or the deadline passes.
func waitForListener(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing is listening on %s", addr)
}

// TestMCPIsStatelessOverHTTP is the guard for WithStateLess.
//
// mcp-go's default session manager rejects any request that arrives without the
// session id it issued during initialize, answering a bare tools/list with a 404
// "Invalid session ID". A client that probes before handshaking — MCPHub and
// most inspector tooling do — reads that as a broken server rather than as a
// protocol error. Stateless mode also means replicas need no sticky routing,
// which costs nothing here because the command tree is rebuilt per call.
func TestMCPIsStatelessOverHTTP(t *testing.T) {
	base, stop := serveWhoami(t)
	defer stop()

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	req, err := http.NewRequest(http.MethodPost, base+"/mcp", body)
	if err != nil {
		t.Fatalf("could not build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	// Deliberately no Mcp-Session-Id: that is the point of the test.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list without a session returned %d, want 200", resp.StatusCode)
	}

	var payload struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("could not decode the tools/list reply: %v", err)
	}
	if len(payload.Result.Tools) != 1 || payload.Result.Tools[0].Name != "whoami" {
		t.Errorf("tools/list returned %+v, want just whoami", payload.Result.Tools)
	}
}

// TestHealthzAnswersWithoutAnMCPHandshake pins the probe route. It has to be
// registered ahead of the catch-all "/" handler, or a liveness probe lands on
// the MCP handler and gets a protocol error instead of "ok".
func TestHealthzAnswersWithoutAnMCPHandshake(t *testing.T) {
	base, stop := serveWhoami(t)
	defer stop()

	resp, err := http.Get(base + "/healthz") //nolint:noctx
	if err != nil {
		t.Fatalf("healthz failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz returned %d, want 200", resp.StatusCode)
	}
	buf := make([]byte, 16)
	n, _ := resp.Body.Read(buf)
	if got := strings.TrimSpace(string(buf[:n])); got != "ok" {
		t.Errorf("healthz body = %q, want ok", got)
	}
}

// TestToolCallCarriesTheCallerIdentity is the end-to-end identity assertion.
//
// It uses the same client and the same per-request header hook that
// internal/agent/pkg/mcpclient uses, so what is verified here is the whole
// path a real tool call takes: the client reads an identity off the request
// context, the transport puts it on the request context on the other side, and
// the command finds it there. Every hop is a place a value can be silently
// dropped, and a dropped identity does not fail loudly — it makes the tool act
// as nobody.
func TestToolCallCarriesTheCallerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   string
	}{
		{name: "identity travels", header: "ou_2f8d0a4c9b7e4f1a", want: "identity=ou_2f8d0a4c9b7e4f1a"},
		{name: "no identity is anonymous", want: "identity="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, stop := serveWhoami(t)
			defer stop()

			cli, err := mcpclient.NewStreamableHttpClient(base+"/mcp",
				mcphttp.WithHTTPHeaderFunc(func(context.Context) map[string]string {
					if tc.header == "" {
						return nil
					}
					return map[string]string{"x-user-id": tc.header}
				}))
			if err != nil {
				t.Fatalf("could not build the client: %v", err)
			}
			defer cli.Close() //nolint:errcheck

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			if err := cli.Start(ctx); err != nil {
				t.Fatalf("could not start the transport: %v", err)
			}
			if _, err := cli.Initialize(ctx, mcp.InitializeRequest{
				Params: mcp.InitializeParams{
					ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
					ClientInfo:      mcp.Implementation{Name: "mcpserver-test", Version: "1.0.0"},
				},
			}); err != nil {
				t.Fatalf("initialize failed: %v", err)
			}

			res, err := cli.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: "whoami"},
			})
			if err != nil {
				t.Fatalf("tools/call failed: %v", err)
			}

			if got := toolResultText(t, res); got != tc.want {
				t.Errorf("command saw %q, want %q", got, tc.want)
			}
		})
	}
}

// toolResultText flattens a tool result into its text.
func toolResultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()

	var b strings.Builder
	for _, c := range res.Content {
		text, ok := c.(mcp.TextContent)
		if !ok {
			t.Fatalf("unexpected content type %T", c)
		}
		b.WriteString(text.Text)
	}
	return strings.TrimSpace(b.String())
}

// TestSREOnlyToolRunsForACallerTheDeploymentGrants is the end-to-end role
// assertion, and the one that would have caught the live failure.
//
// A tool call that reaches the command tree and is refused there comes back to
// the model as "requires SRE role", which reads like a fact about the caller
// rather than like a bug in this service: the model repeated it to the user, and
// the release it had been approved to trigger never happened. Nothing in the
// path fails loudly, because a refused tool call is a perfectly ordinary result.
//
// The tree here installs the gate the real one installs, so what is asserted is
// the whole path: the caller's identity arrives, this deployment's own role list
// decides, and an SRE-only tool runs for a caller that list calls an SRE.
func TestSREOnlyToolRunsForACallerTheDeploymentGrants(t *testing.T) {
	base, stop := serveTree(t, roleRuntime(),
		func(cmd *cobra.Command, _ []string) error {
			return command.ValidateCommands(cmd, runtime.From(cmd.Context()))
		},
		func(out runtime.Responder) []*cobra.Command {
			return []*cobra.Command{roleCmd(out)}
		})
	defer stop()

	cli, err := mcpclient.NewStreamableHttpClient(base+"/mcp",
		mcphttp.WithHTTPHeaderFunc(func(context.Context) map[string]string {
			return map[string]string{"x-user-id": "ou_sre"}
		}))
	if err != nil {
		t.Fatalf("could not build the client: %v", err)
	}
	defer cli.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := cli.Start(ctx); err != nil {
		t.Fatalf("could not start the transport: %v", err)
	}
	if _, err := cli.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "mcpserver-test", Version: "1.0.0"},
		},
	}); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}

	res, err := cli.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "whoroles"},
	})
	if err != nil {
		t.Fatalf("tools/call failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("an SRE-only tool was refused for a caller this deployment calls an SRE: %s",
			toolResultText(t, res))
	}
	if got := toolResultText(t, res); got != "sre=true admin=true" {
		t.Errorf("the command saw %q, want the roles the deployment resolved", got)
	}
}

// TestToolFailureWithoutStdoutReportsSomething covers the one path where the
// middleware reads stderr. A command that fails without printing anything is
// the ordinary case for a tool that hits an error before it renders one, and a
// tool error with an empty message tells the model nothing at all.
func TestToolFailureWithoutStdoutReportsSomething(t *testing.T) {
	addr := freeAddr(t)

	factory := func() *cobra.Command {
		var root *cobra.Command
		out := newResponder(func() *cobra.Command { return root })
		root = &cobra.Command{Use: "/", SilenceErrors: true, SilenceUsage: true}
		root.AddCommand(&cobra.Command{
			Use:           "boom",
			Annotations:   map[string]string{chat.MCPTool: chat.True},
			SilenceErrors: true,
			SilenceUsage:  true,
			// A command that returns an error without writing: cobrax turns the
			// error into ToolOutput.StdErr, and the middleware has to relay it.
			RunE: func(cmd *cobra.Command, _ []string) error {
				_ = out
				return fmt.Errorf("boom: the record does not exist")
			},
		})
		return root
	}

	srv, err := cobrax.NewMCPServer(
		cobrax.MCPOptions{Enabled: true, Addr: addr, Name: "oai-chat-apiserver", Version: "test"},
		factory,
		cobrax.WithSelectors(selector()),
		cobrax.WithTransport(newTransport(&options.MCPOptions{Addr: addr}, roleRuntime())),
	)
	if err != nil {
		t.Fatalf("NewMCPServer returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	waitForListener(t, addr)

	cli, err := mcpclient.NewStreamableHttpClient("http://" + addr + "/mcp")
	if err != nil {
		t.Fatalf("could not build the client: %v", err)
	}
	defer cli.Close() //nolint:errcheck

	callCtx, callCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer callCancel()

	if err := cli.Start(callCtx); err != nil {
		t.Fatalf("could not start the transport: %v", err)
	}
	if _, err := cli.Initialize(callCtx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "mcpserver-test", Version: "1.0.0"},
		},
	}); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}

	res, err := cli.CallTool(callCtx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "boom"},
	})
	if err != nil {
		t.Fatalf("tools/call failed at the protocol level: %v", err)
	}
	if !res.IsError {
		t.Fatalf("a failing command was reported as a successful tool call: %+v", res)
	}
	if text := toolResultText(t, res); !strings.Contains(text, "does not exist") {
		t.Errorf("tool error text = %q, want the command's message", text)
	}
}
