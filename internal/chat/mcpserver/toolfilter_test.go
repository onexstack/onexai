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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/feizaonet/cobrax"
	mcpclient "github.com/mark3labs/mcp-go/client"
	mcphttp "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/roleset"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
	"github.com/onexstack/onexai/pkg/options"
)

// staticGrant is a stand-in for the store-backed resolver. It answers with the
// selection it was built around for an agent it covers, and with nothing for one
// it does not — modelling the real resolver's contract, where a caller that
// names no agent, or names agents no rule addresses, has been granted nothing.
type staticGrant struct {
	covered map[string]struct{}
	sel     runtime.CommandSelection
}

func (g staticGrant) Resolve(_ context.Context, agents []string) runtime.CommandSelection {
	if len(agents) == 0 {
		return runtime.CommandSelection{}
	}
	for _, a := range agents {
		if _, ok := g.covered[a]; ok {
			return g.sel
		}
	}
	return runtime.CommandSelection{}
}

// grantedTo builds a selection admitting exactly the named commands.
func grantedTo(names ...string) runtime.CommandSelection {
	sel := runtime.CommandSelection{Names: make(map[string]struct{}, len(names))}
	for _, n := range names {
		sel.Names[n] = struct{}{}
	}
	return sel
}

// grantingRuntime builds a runtime whose grant resolver covers one agent.
func grantingRuntime(covered string, sel runtime.CommandSelection) *runtime.StaticRuntime {
	return &runtime.StaticRuntime{
		Roles:         roleset.NewStatic(map[string]struct{}{"ou_sre": {}}, nil),
		AgentCommands: staticGrant{covered: map[string]struct{}{covered: {}}, sel: sel},
	}
}

// leafTool builds a command that registers as an MCP tool and does nothing.
func leafTool(use string) *cobra.Command {
	return &cobra.Command{
		Use:           use,
		Short:         use,
		Annotations:   map[string]string{chat.MCPTool: chat.True},
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          func(*cobra.Command, []string) error { return nil },
	}
}

// group builds a top-level command with the given leaf subcommands, so a leaf's
// tool name is "<group>_<leaf>" — the shape the real tree has and the shape
// TopLevelCommand maps back to a grant.
func group(name string, leaves ...string) *cobra.Command {
	c := &cobra.Command{Use: name, Short: name, SilenceErrors: true, SilenceUsage: true}
	for _, l := range leaves {
		c.AddCommand(leafTool(l))
	}
	return c
}

// serveGrantedTree starts the real transport over a small tree, with the same
// tool filter New installs and the same grant gate the real root installs, so a
// tool that is filtered from the list and a tool that is refused on call are
// exercised on the whole path.
func serveGrantedTree(t *testing.T, sr *runtime.StaticRuntime, commands func(runtime.Responder) []*cobra.Command) (string, func()) {
	t.Helper()

	addr := freeAddr(t)

	factory := func() *cobra.Command {
		var root *cobra.Command
		out := newResponder(func() *cobra.Command { return root })
		root = &cobra.Command{
			Use:               "/",
			SilenceErrors:     true,
			SilenceUsage:      true,
			PersistentPreRunE: grantGate(sr),
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
	attachToolFilter(srv, sr, nil)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Start(ctx) }()

	waitForListener(t, addr)
	return "http://" + addr, func() {
		cancel()
		select {
		case <-served:
		case <-time.After(15 * time.Second):
			t.Error("the MCP listener did not shut down after its context was cancelled")
		}
	}
}

// grantGate is the pair the real root runs: the role gate, then the grant gate.
func grantGate(sr *runtime.StaticRuntime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		if err := command.ValidateCommands(cmd, runtime.From(cmd.Context())); err != nil {
			return err
		}
		return command.ValidateGranted(cmd, command.Selection(cmd.Context(), sr))
	}
}

// toolsListNames fetches tools/list over HTTP with the given x-agent-names header
// (empty for none) and returns the tool names, sorted as the server returns them.
func toolsListNames(t *testing.T, base, agents string) []string {
	t.Helper()

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	req, err := http.NewRequest(http.MethodPost, base+"/mcp", body)
	if err != nil {
		t.Fatalf("could not build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if agents != "" {
		req.Header.Set("x-agent-names", agents)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list returned %d, want 200", resp.StatusCode)
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

	names := make([]string, 0, len(payload.Result.Tools))
	for _, tl := range payload.Result.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// TestAgentNamesParsesTheHeader pins the wire form: a comma-separated list,
// trimmed, with empties dropped, so a trailing comma or a padded list still names
// only real agents.
func TestAgentNamesParsesTheHeader(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   []string
	}{
		{header: "", want: nil},
		{header: "bke-agent", want: []string{"bke-agent"}},
		{header: "a,b", want: []string{"a", "b"}},
		{header: " a , b ,", want: []string{"a", "b"}},
		{header: "  ", want: nil},
	} {
		t.Run(tc.header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.header != "" {
				req.Header.Set("x-agent-names", tc.header)
			}
			got := agentNames(req)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("agentNames(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

// TestAttributeMCPCallerSetsResolvedAgents is the identity half of the filtering
// contract: the header has to reach the runtime as the agents the command tree
// filters by, or the tool list is never narrowed.
func TestAttributeMCPCallerSetsResolvedAgents(t *testing.T) {
	tr := newTransport(&options.MCPOptions{}, roleRuntime())

	var dr runtime.DynamicRuntime
	handler := tr.attributeMCPCaller(identityRecorder(&dr))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("x-agent-names", "bke-agent")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if dr.ResolvedAgents == nil {
		t.Fatal("ResolvedAgents is nil; the tool list would not be filtered")
	}
	res, err := dr.ResolvedAgents(context.Background(), &dr)
	if err != nil {
		t.Fatalf("ResolvedAgents() = %v", err)
	}
	if len(res.Agents) != 1 || res.Agents[0] != "bke-agent" {
		t.Errorf("ResolvedAgents() = %v, want [bke-agent]", res.Agents)
	}

	// No header leaves it nil: a caller that does not say which agents it runs
	// as must not have the tool list taken away.
	var anon runtime.DynamicRuntime
	tr.attributeMCPCaller(identityRecorder(&anon)).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if anon.ResolvedAgents != nil {
		t.Error("ResolvedAgents was set without a header; a caller that declares no agent must stay nameless")
	}
}

// TestToolListNarrowsToTheCallersGrants is the end-to-end assertion for the tool
// list. It is what makes a chat MCP server bound to an agent advertise only the
// tools of the commands that agent was granted — and it pins the deny-by-default
// edge, where a caller naming no agent, or an agent no rule covers, is shown
// nothing but the tools no grant reaches: here the askai-mcp stand-in, which has
// no catalogued command behind it and so survives an empty grant list.
func TestToolListNarrowsToTheCallersGrants(t *testing.T) {
	sr := grantingRuntime("bke-agent", grantedTo("duty"))
	base, stop := serveGrantedTree(t, sr, func(out runtime.Responder) []*cobra.Command {
		return []*cobra.Command{group("duty", "list"), group("job", "create"), leafTool("askai-mcp")}
	})
	defer stop()

	for _, tc := range []struct {
		name   string
		agents string
		want   []string
	}{
		{name: "a granted agent sees only its tools", agents: "bke-agent", want: []string{"askai-mcp", "duty_list"}},
		{name: "no agent named means no grant and so no tools", agents: "", want: []string{"askai-mcp"}},
		{name: "an agent no rule covers is granted nothing", agents: "stranger-agent", want: []string{"askai-mcp"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toolsListNames(t, base, tc.agents)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("tools/list with x-agent-names=%q = %v, want %v", tc.agents, got, tc.want)
			}
		})
	}
}

// TestToolCallGrantGateOverMCP pins the call half: a tool called directly by
// name is refused on the server when the calling agents were not granted its
// command — including when the caller names no agent at all, which is a caller
// with no grants rather than a caller with all of them. A tool no catalogued
// command backs is the exception, and stays callable.
func TestToolCallGrantGateOverMCP(t *testing.T) {
	sr := grantingRuntime("bke-agent", grantedTo("duty"))
	base, stop := serveGrantedTree(t, sr, func(out runtime.Responder) []*cobra.Command {
		return []*cobra.Command{group("duty", "list"), group("job", "create"), leafTool("askai-mcp")}
	})
	defer stop()

	for _, tc := range []struct {
		name      string
		agents    string
		tool      string
		wantError bool
	}{
		{name: "a granted tool runs as the named agent", agents: "bke-agent", tool: "duty_list"},
		{name: "an ungranted tool is refused as the named agent", agents: "bke-agent", tool: "job_create", wantError: true},
		{name: "an ungranted tool is refused for a caller naming no agent", tool: "job_create", wantError: true},
		{name: "a tool no catalogued command backs is not gated", tool: "askai-mcp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callToolWithAgents(t, base, tc.agents, tc.tool, tc.wantError)
		})
	}
}

func callToolWithAgents(t *testing.T, base, agents, tool string, wantError bool) {
	t.Helper()

	cli, err := mcpclient.NewStreamableHttpClient(base+"/mcp",
		mcphttp.WithHTTPHeaderFunc(func(context.Context) map[string]string {
			if agents == "" {
				return nil
			}
			return map[string]string{"x-agent-names": agents}
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
		Params: mcp.CallToolParams{Name: tool},
	})
	if err != nil {
		t.Fatalf("tools/call failed at the protocol level: %v", err)
	}
	if res.IsError != wantError {
		t.Fatalf("tools/call %q with x-agent-names=%q IsError = %t, want %t: %s",
			tool, agents, res.IsError, wantError, toolResultText(t, res))
	}
	if wantError && !strings.Contains(toolResultText(t, res), "not enabled for this conversation's agent") {
		t.Errorf("refusal text = %q, want the grant gate's message", toolResultText(t, res))
	}
}
