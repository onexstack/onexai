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
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
)

var errFake = errors.New("fake tool failure")

// fakeStore serves a fixed set of MCP rows. Only MCPs is implemented; the
// embedded interface panics loudly if the manager ever reaches for another
// accessor.
type fakeStore struct {
	store.AIStore
	mcps []*model.MCPM
}

func (s fakeStore) MCPs() store.MCPStore { return fakeMCPStore{items: s.mcps} }

type fakeMCPStore struct {
	store.MCPStore
	items []*model.MCPM
}

func (s fakeMCPStore) List(context.Context, *where.Options) (int64, []*model.MCPM, error) {
	return int64(len(s.items)), s.items, nil
}

// fakeConn records that it was closed.
type fakeConn struct {
	mu     sync.Mutex
	closed bool
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// fakeDialer stands in for the network. It records every server it is asked to
// dial and hands back per-server tools or errors.
type fakeDialer struct {
	mu      sync.Mutex
	dialed  []string
	agents  [][]string
	tools   map[string][]tool.BaseTool
	errors  map[string]error
	conns   []*fakeConn
	blockOn map[string]time.Duration
}

func newFakeDialer() *fakeDialer {
	return &fakeDialer{
		tools:   map[string][]tool.BaseTool{},
		errors:  map[string]error{},
		blockOn: map[string]time.Duration{},
	}
}

func (d *fakeDialer) Dial(_ context.Context, m *model.MCPM, agents []string, _ time.Duration) (mcpConn, []tool.BaseTool, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, m.Name)
	d.agents = append(d.agents, append([]string(nil), agents...))
	block, hasBlock := d.blockOn[m.Name]
	err := d.errors[m.Name]
	ts := d.tools[m.Name]
	d.mu.Unlock()

	if hasBlock {
		time.Sleep(block)
	}
	if err != nil {
		return nil, nil, err
	}
	conn := &fakeConn{}
	d.mu.Lock()
	d.conns = append(d.conns, conn)
	d.mu.Unlock()
	return conn, ts, nil
}

func (d *fakeDialer) dialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.dialed)
}

// dialedAgents returns the agents each dial was asked for, in dial order, so a
// test can pin that a run's selection reaches discovery rather than only the
// tool call.
func (d *fakeDialer) dialedAgents() [][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.agents
}

func (d *fakeDialer) conn(i int) *fakeConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[i]
}

// server builds an enabled MCP row.
func server(name string) *model.MCPM {
	return &model.MCPM{Name: name, Status: model.StatusEnabled, Protocol: ProtocolHTTP, URL: "http://" + name + ":8081/mcp"}
}

// waitFor polls until cond holds, failing the test after a second.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestManagerEmptyServersReturnsNil(t *testing.T) {
	dialer := newFakeDialer()
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("zke")}},
		Options{Enabled: true, Dialer: dialer})

	for _, servers := range [][]string{nil, {}, {"  ", ""}} {
		if got := m.Tools(servers, nil); got != nil {
			t.Fatalf("Tools(%q) = %v, want nil: an empty selection means no MCP tools", servers, got)
		}
	}
	if dialer.dialCount() != 0 {
		t.Fatalf("dialed %v, want nothing dialed for an empty selection", dialer.dialed)
	}
}

func TestManagerDisabledReturnsNil(t *testing.T) {
	dialer := newFakeDialer()
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("zke")}},
		Options{Enabled: false, Dialer: dialer})

	if got := m.Tools([]string{"zke"}, nil); got != nil {
		t.Fatalf("Tools = %v, want nil when disabled", got)
	}
	if dialer.dialCount() != 0 {
		t.Fatal("a disabled manager must not dial anything")
	}
}

func TestManagerSkipsDisabledUnknownAndUnreachable(t *testing.T) {
	dialer := newFakeDialer()
	dialer.errors["down"] = errors.New("connection refused")

	disabled := server("off")
	disabled.Status = model.StatusDisabled

	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{
		server("down"), disabled, server("up"),
	}}, Options{Enabled: true, Dialer: dialer})
	dialer.tools["up"] = []tool.BaseTool{&recordedTool{wire: "up.ping", out: "pong"}}

	// "absent" is referenced by an agent but has no row at all.
	got := m.Tools([]string{"down", "off", "absent", "up"}, nil)

	if len(got) != 1 {
		t.Fatalf("got %d tools, want only the reachable server's tool", len(got))
	}
	for _, name := range dialer.dialed {
		if name == "off" {
			t.Fatal("a disabled server must never be dialed")
		}
		if name == "absent" {
			t.Fatal("a server with no row must never be dialed")
		}
	}
}

func TestManagerBuildsOncePerServerSet(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["a"] = []tool.BaseTool{&recordedTool{wire: "a.x", out: "1"}}
	dialer.tools["b"] = []tool.BaseTool{&recordedTool{wire: "b.y", out: "2"}}

	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("a"), server("b")}},
		Options{Enabled: true, Dialer: dialer, TTL: time.Hour})

	// Order and repetition must not matter: the same set is one cache entry.
	for _, servers := range [][]string{{"a", "b"}, {"b", "a"}, {"b", "a", "a"}} {
		if got := m.Tools(servers, nil); len(got) != 2 {
			t.Fatalf("Tools(%q) = %d tools, want 2", servers, len(got))
		}
	}
	if n := dialer.dialCount(); n != 2 {
		t.Fatalf("dialed %d times (%v), want each server dialed once", n, dialer.dialed)
	}
}

func TestManagerSingleServerIsItsOwnEntry(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["a"] = []tool.BaseTool{&recordedTool{wire: "a.x", out: "1"}}
	dialer.tools["b"] = []tool.BaseTool{&recordedTool{wire: "b.y", out: "2"}}

	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("a"), server("b")}},
		Options{Enabled: true, Dialer: dialer, TTL: time.Hour})

	if got := m.Tools([]string{"a"}, nil); len(got) != 1 {
		t.Fatalf("got %d tools for one server, want 1", len(got))
	}
	if got := m.Tools([]string{"a", "b"}, nil); len(got) != 2 {
		t.Fatalf("got %d tools for two servers, want 2", len(got))
	}
}

// TestManagerServesStaleWhileRebuilding covers the TTL contract: a run that
// arrives after expiry is answered from cache immediately, and the rebuild
// happens in the background rather than on its critical path.
func TestManagerServesStaleWhileRebuilding(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["a"] = []tool.BaseTool{&recordedTool{wire: "a.x", out: "1"}}

	now := time.Now()
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("a")}},
		Options{Enabled: true, Dialer: dialer, TTL: time.Minute})
	m.now = func() time.Time { return now }

	if got := m.Tools([]string{"a"}, nil); len(got) != 1 {
		t.Fatalf("first call returned %d tools, want 1", len(got))
	}

	now = now.Add(2 * time.Minute) // past the TTL
	if got := m.Tools([]string{"a"}, nil); len(got) != 1 {
		t.Fatalf("call after expiry returned %d tools, want the stale set served", len(got))
	}
	waitFor(t, "the background rebuild", func() bool { return dialer.dialCount() == 2 })
}

func TestManagerRetiresConnectionsOnRebuild(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["a"] = []tool.BaseTool{&recordedTool{wire: "a.x", out: "1"}}

	now := time.Now()
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("a")}},
		Options{Enabled: true, Dialer: dialer, TTL: time.Minute})
	m.now = func() time.Time { return now }
	m.closeGrace = 0 // close retired connections at once

	m.Tools([]string{"a"}, nil)
	now = now.Add(2 * time.Minute)
	m.Tools([]string{"a"}, nil)

	waitFor(t, "the retired connection to be closed", func() bool { return dialer.conn(0).isClosed() })
	if dialer.conn(1).isClosed() {
		t.Fatal("the connection now in use must stay open")
	}
}

// TestManagerKeepsStaleSetWhenRebuildFails pins the failure policy: a server
// that stops answering must not cost the tools it used to provide.
func TestManagerKeepsStaleSetWhenRebuildFails(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["a"] = []tool.BaseTool{&recordedTool{wire: "a.x", out: "1"}}

	now := time.Now()
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("a")}},
		Options{Enabled: true, Dialer: dialer, TTL: time.Minute})
	m.now = func() time.Time { return now }

	m.Tools([]string{"a"}, nil)
	dialer.errors["a"] = errors.New("connection refused")

	now = now.Add(2 * time.Minute)
	m.Tools([]string{"a"}, nil)
	waitFor(t, "the failing rebuild", func() bool { return dialer.dialCount() == 2 })

	// Still served, and not retried on every call: the backoff holds.
	for i := 0; i < 5; i++ {
		if got := m.Tools([]string{"a"}, nil); len(got) != 1 {
			t.Fatalf("call %d returned %d tools, want the previous set kept", i, len(got))
		}
	}
	if n := dialer.dialCount(); n != 2 {
		t.Fatalf("dialed %d times, want the failed set retried only after the backoff", n)
	}
}

// TestManagerSuppressesToolErrors is the guard for eino's ToolsNode behaviour:
// it aborts the whole run when a tool returns an error, so a failing MCP call
// must reach the model as text instead.
func TestManagerSuppressesToolErrors(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["a"] = []tool.BaseTool{&recordedTool{wire: "kubernetes_pod.logs", out: "", err: errFake}}

	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("a")}},
		Options{Enabled: true, Dialer: dialer})

	tools := m.Tools([]string{"a"}, nil)
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	inner, ok := tools[0].(tool.InvokableTool)
	if !ok {
		t.Fatal("the assembled tool must be invokable")
	}

	out, err := inner.InvokableRun(context.Background(), "{}")
	if err != nil {
		t.Fatalf("InvokableRun returned an error (%v); it would abort the whole run", err)
	}
	if !strings.Contains(out, "kubernetes_pod_logs") || !strings.Contains(out, errFake.Error()) {
		t.Fatalf("result %q, want it to name the tool and carry the failure", out)
	}
}

func TestManagerSanitizesAndUniquifiesNames(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["a"] = []tool.BaseTool{
		&recordedTool{wire: "kubernetes_pod.logs", out: "1"},
		// Shadows a built-in name, which is reserved.
		&recordedTool{wire: "current_time", out: "2"},
		// Nothing usable survives sanitising.
		&recordedTool{wire: "...", out: "3"},
	}

	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("a")}},
		Options{Enabled: true, Dialer: dialer, Reserved: []string{"current_time"}})

	tools := m.Tools([]string{"a"}, nil)
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2 (the unusable name must be dropped)", len(tools))
	}

	var names []string
	for _, tl := range tools {
		info, err := tl.Info(context.Background())
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		names = append(names, info.Name)
		if !legalToolName.MatchString(info.Name) {
			t.Fatalf("name %q is not a legal function name", info.Name)
		}
	}
	if names[0] != "current_time_2" || names[1] != "kubernetes_pod_logs" {
		t.Fatalf("got names %v, want the dotted name sanitised and the built-in shadow renamed", names)
	}
}

// TestManagerDeterministicNames pins that the assignment does not depend on the
// order the server lists its tools in.
func TestManagerDeterministicNames(t *testing.T) {
	namesFor := func(order []string) []string {
		var raw []tool.BaseTool
		for _, w := range order {
			raw = append(raw, &recordedTool{wire: w, out: "x"})
		}
		dialer := newFakeDialer()
		dialer.tools["a"] = raw
		m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("a")}},
			Options{Enabled: true, Dialer: dialer})

		var got []string
		for _, tl := range m.Tools([]string{"a"}, nil) {
			info, err := tl.Info(context.Background())
			if err != nil {
				t.Fatalf("Info: %v", err)
			}
			got = append(got, info.Name)
		}
		return got
	}

	first := namesFor([]string{"b.tool", "a.tool"})
	second := namesFor([]string{"a.tool", "b.tool"})
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Fatalf("names differ with listing order: %v vs %v", first, second)
	}
	if len(first) != 2 || first[0] != "a_tool" || first[1] != "b_tool" {
		t.Fatalf("got %v, want the tools sorted by wire name", first)
	}
}

func TestManagerCloseReleasesConnections(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["a"] = []tool.BaseTool{&recordedTool{wire: "a.x", out: "1"}}

	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("a")}},
		Options{Enabled: true, Dialer: dialer})
	m.Tools([]string{"a"}, nil)

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !dialer.conn(0).isClosed() {
		t.Fatal("Close must release the cached connection")
	}
	if got := m.Tools([]string{"a"}, nil); len(got) != 1 {
		t.Fatal("the manager must still work after Close; it simply starts empty")
	}
}

// TestToolsCacheIsKeyedByAgents pins the second cache dimension. A server may
// publish a different tool set to different agents — the chat server narrows its
// tools to the commands the agents were granted — so two runs over the same
// servers but different agents must not share a discovery, and the agents have to
// reach the dialer for the server to scope them at all.
func TestToolsCacheIsKeyedByAgents(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["chat"] = []tool.BaseTool{&recordedTool{wire: "chat.duty_list", out: "ok"}}

	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("chat")}},
		Options{Enabled: true, Dialer: dialer})

	// Same servers, same agents: one dial, the second served from cache.
	m.Tools([]string{"chat"}, []string{"a"})
	m.Tools([]string{"chat"}, []string{"a"})
	if got := dialer.dialCount(); got != 1 {
		t.Fatalf("dialed %d times for one (servers, agents) pair, want 1", got)
	}

	// Same servers, different agents: a second dial, because the tool set may differ.
	m.Tools([]string{"chat"}, []string{"b"})
	if got := dialer.dialCount(); got != 2 {
		t.Fatalf("dialed %d times; a different agent set must not share the cache entry", got)
	}

	got := dialer.dialedAgents()
	if len(got) != 2 || strings.Join(got[0], ",") != "a" || strings.Join(got[1], ",") != "b" {
		t.Fatalf("dials asked for agents %v, want [[a] [b]]: the selection must reach discovery", got)
	}
}

func TestInvalidateForcesRediscovery(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["zke"] = []tool.BaseTool{&recordedTool{wire: "zke.ping", out: "pong"}}
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("zke")}},
		Options{Enabled: true, Dialer: dialer, TTL: time.Hour})

	if got := m.Tools([]string{"zke"}, []string{"helper"}); len(got) != 1 {
		t.Fatalf("first Tools returned %d tools, want 1", len(got))
	}
	if dialer.dialCount() != 1 {
		t.Fatalf("dial count after the first run = %d, want 1", dialer.dialCount())
	}

	m.Invalidate()
	if got := m.Tools([]string{"zke"}, []string{"helper"}); len(got) != 1 {
		t.Fatalf("Tools after Invalidate returned %d tools, want 1", len(got))
	}
	if dialer.dialCount() != 2 {
		t.Fatalf("dial count after Invalidate = %d, want 2 (the cache was not dropped)", dialer.dialCount())
	}
}

func TestInvalidateAgentLeavesOtherAgentsCached(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["zke"] = []tool.BaseTool{&recordedTool{wire: "zke.ping", out: "pong"}}
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("zke")}},
		Options{Enabled: true, Dialer: dialer, TTL: time.Hour})

	m.Tools([]string{"zke"}, []string{"a"})
	m.Tools([]string{"zke"}, []string{"b"})
	if dialer.dialCount() != 2 {
		t.Fatalf("dial count = %d, want 2 after warming two agent sets", dialer.dialCount())
	}

	m.InvalidateAgent("a")

	m.Tools([]string{"zke"}, []string{"a"})
	if dialer.dialCount() != 3 {
		t.Fatalf("dial count after invalidating a = %d, want 3", dialer.dialCount())
	}
	m.Tools([]string{"zke"}, []string{"b"})
	if dialer.dialCount() != 3 {
		t.Fatalf("dial count = %d, want 3 (b must stay cached)", dialer.dialCount())
	}
}

func TestSettleWindowShortensRebuiltTTL(t *testing.T) {
	dialer := newFakeDialer()
	dialer.tools["zke"] = []tool.BaseTool{&recordedTool{wire: "zke.ping", out: "pong"}}
	m := NewManager(context.Background(), fakeStore{mcps: []*model.MCPM{server("zke")}},
		Options{Enabled: true, Dialer: dialer, TTL: 5 * time.Minute})

	now := time.Unix(1_000, 0)
	m.now = func() time.Time { return now }

	m.Tools([]string{"zke"}, []string{"helper"})
	key, _ := cacheKey([]string{"zke"}, []string{"helper"})
	m.mu.Lock()
	normalExp := m.entries[key].exp
	m.mu.Unlock()
	if want := now.Add(5 * time.Minute); !normalExp.Equal(want) {
		t.Fatalf("normal expiry = %v, want %v", normalExp, want)
	}

	m.Invalidate()
	now = now.Add(time.Second)
	m.Tools([]string{"zke"}, []string{"helper"})

	m.mu.Lock()
	settledExp := m.entries[key].exp
	m.mu.Unlock()
	if want := now.Add(settleTTL); !settledExp.Equal(want) {
		t.Fatalf("settled expiry = %v, want %v (a write must not pin the answer for the full TTL)", settledExp, want)
	}
}
