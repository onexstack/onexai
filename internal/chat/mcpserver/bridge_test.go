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
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/onexstack/onexai/internal/chat/pkg/agentcmd"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	"github.com/onexstack/onexai/pkg/options"
)

// mutableCustomSource is the per-agent custom-command source the filter reads.
// It exists so a test can change an agent's binding without rebuilding the
// resolver.
type mutableCustomSource struct {
	mu      sync.Mutex
	byAgent map[string][]string
}

func (s *mutableCustomSource) set(agent string, names ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAgent == nil {
		s.byAgent = make(map[string][]string)
	}
	s.byAgent[agent] = append([]string(nil), names...)
}

func (s *mutableCustomSource) CustomCommandsOf(_ context.Context, agent string) ([]runtime.CustomCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := s.byAgent[agent]
	out := make([]runtime.CustomCommand, 0, len(names))
	for _, name := range names {
		out = append(out, runtime.CustomCommand{Name: name, Type: "prompt"})
	}
	return out, nil
}

// catalogLister returns the catalog a test currently wants the bridge to see.
type catalogLister struct {
	mu      sync.Mutex
	customs []runtime.CustomCommand
	err     error
	calls   int
}

func (l *catalogLister) list(context.Context) ([]runtime.CustomCommand, error) {
	l.mu.Lock()
	l.calls++
	err := l.err
	out := append([]runtime.CustomCommand(nil), l.customs...)
	l.mu.Unlock()
	return out, err
}

func (l *catalogLister) set(customs ...runtime.CustomCommand) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.customs = append([]runtime.CustomCommand(nil), customs...)
	l.err = nil
}

func (l *catalogLister) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.err = err
}

// toolNames is every tool the current server publishes.
func toolNames(tools []*mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

func TestBridgeReloadsCustomTools(t *testing.T) {
	addr := freeAddr(t)
	b, err := New(&options.MCPOptions{
		Enabled: true, Addr: addr, Name: "test", Version: "1",
		// The run loop is exercised by the HTTP test below; here reload() is
		// called directly so the assertion is not timing dependent.
		RefreshInterval: time.Hour,
	}, &runtime.StaticRuntime{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b == nil {
		t.Fatal("New returned nil for an enabled listener")
	}

	catalog := &catalogLister{}
	b.list = catalog.list

	catalog.set(runtime.CustomCommand{Name: "alpha", Type: "prompt"})
	b.reload()
	if got := toolNames(b.Tools()); !contains(got, "alpha") {
		t.Fatalf("alpha is not a registered tool: %v", got)
	}

	catalog.set(
		runtime.CustomCommand{Name: "alpha", Type: "prompt"},
		runtime.CustomCommand{Name: "beta", Aliases: []string{"b"}},
	)
	b.reload()
	if got := toolNames(b.Tools()); !contains(got, "beta") {
		t.Fatalf("beta did not appear after reload: %v", got)
	}

	catalog.set(runtime.CustomCommand{Name: "beta", Aliases: []string{"b"}})
	b.reload()
	if got := toolNames(b.Tools()); contains(got, "alpha") {
		t.Fatalf("alpha survived its removal: %v", got)
	}
}

func TestBridgeKeepsToolsWhenListingFails(t *testing.T) {
	addr := freeAddr(t)
	b, err := New(&options.MCPOptions{
		Enabled: true, Addr: addr, Name: "test", Version: "1", RefreshInterval: time.Hour,
	}, &runtime.StaticRuntime{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	catalog := &catalogLister{}
	b.list = catalog.list
	catalog.set(runtime.CustomCommand{Name: "alpha"})
	b.reload()

	catalog.fail(errors.New("agent service unavailable"))
	b.reload()

	if got := toolNames(b.Tools()); !contains(got, "alpha") {
		t.Fatalf("a listing failure withdrew the existing tool: %v", got)
	}
}

func TestBridgeDoesNotRebuildUnchangedCatalog(t *testing.T) {
	addr := freeAddr(t)
	b, err := New(&options.MCPOptions{
		Enabled: true, Addr: addr, Name: "test", Version: "1", RefreshInterval: time.Hour,
	}, &runtime.StaticRuntime{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	catalog := &catalogLister{}
	b.list = catalog.list
	catalog.set(
		runtime.CustomCommand{Name: "beta", Aliases: []string{"b2", "b1"}},
		runtime.CustomCommand{Name: "alpha"},
	)
	b.reload()
	afterFirst := b.reloads.Load()

	// Same set, different order and alias order: the canonical form must make
	// the second read a no-op rather than a rebuild.
	catalog.set(
		runtime.CustomCommand{Name: "alpha"},
		runtime.CustomCommand{Name: "beta", Aliases: []string{"b1", "b2"}},
	)
	b.reload()
	if got := b.reloads.Load(); got != afterFirst {
		t.Errorf("reloads = %d, want %d (an unchanged catalog rebuilt the server)", got, afterFirst)
	}
}

// TestBridgeReloadIsVisibleOverHTTP is the end-to-end shape of the bug: a
// command added after startup must appear in tools/list without restarting the
// listener.
func TestBridgeReloadIsVisibleOverHTTP(t *testing.T) {
	addr := freeAddr(t)

	bindings := &mutableCustomSource{}
	bindings.set("helper", "alpha")
	resolver := agentcmd.New(agentcmd.Options{
		Rules:      mcpEmptyRules{},
		Customs:    bindings,
		CustomsTTL: 10 * time.Millisecond,
	})
	sr := &runtime.StaticRuntime{AgentCommands: resolver}

	b, err := New(&options.MCPOptions{
		Enabled: true, Addr: addr, Name: "test", Version: "1", RefreshInterval: time.Hour,
	}, sr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	catalog := &catalogLister{}
	catalog.set(runtime.CustomCommand{Name: "alpha", Type: "prompt"})
	b.list = catalog.list
	b.reload()

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- b.Start(ctx) }()
	defer func() {
		cancel()
		select {
		case <-served:
		case <-time.After(15 * time.Second):
			t.Error("the MCP listener did not shut down")
		}
	}()
	waitForListener(t, addr)
	base := "http://" + addr

	waitForTool(t, base, "helper", "alpha", true)

	// The catalog grows without restarting the process.
	catalog.set(
		runtime.CustomCommand{Name: "alpha", Type: "prompt"},
		runtime.CustomCommand{Name: "beta", Type: "prompt"},
	)
	b.reload()
	bindings.set("helper", "alpha", "beta")
	waitForTool(t, base, "helper", "beta", true)

	// An agent that was not bound to beta still must not see it.
	if got := listToolNames(t, base, "other"); contains(got, "beta") {
		t.Fatalf("an unbound agent can see beta: %v", got)
	}

	// Removing it withdraws the tool.
	catalog.set(runtime.CustomCommand{Name: "alpha", Type: "prompt"})
	b.reload()
	bindings.set("helper", "alpha")
	waitForTool(t, base, "helper", "beta", false)
}

// waitForTool polls tools/list until want is satisfied or the deadline passes.
// The bridge reload is synchronous here, but the resolver caches an agent's
// custom commands for its TTL and refreshes in the background on a stale read,
// so the first request after a binding change can still answer from the old
// value.
func waitForTool(t *testing.T, base, agents, name string, want bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		got = listToolNames(t, base, agents)
		if contains(got, name) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("tool %q presence for agent %q = %t, want %t; tools=%v",
		name, agents, contains(got, name), want, got)
}

// listToolNames asks the real listener for tools as the named agents.
func listToolNames(t *testing.T, base, agents string) []string {
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
	for _, tool := range payload.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestRegisteredCustomToolsFollowTheTree pins two things the old
// customToolNames(customs) got wrong: a prompt custom command is usable on the
// MCP surface (it renders its text instead of starting a turn), and a command
// dropped because its name collides with a built-in must not make that
// built-in tool look like a custom one.
func TestRegisteredCustomToolsFollowTheTree(t *testing.T) {
	source := &commandSource{
		sr: &runtime.StaticRuntime{},
		customs: []runtime.CustomCommand{
			{Name: "alpha", Type: "prompt"},
			{Name: "save", Type: "prompt"}, // save is a catalogued command
		},
	}

	got := registeredCustomTools(source.newTree())
	if got["alpha"] != "alpha" {
		t.Errorf("alpha = %q, want it registered from the prompt command", got["alpha"])
	}
	if name, ok := got["save"]; ok {
		t.Errorf("save was mapped to custom command %q, but the custom command was dropped", name)
	}
}
