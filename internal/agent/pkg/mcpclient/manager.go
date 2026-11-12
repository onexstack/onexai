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

// Package mcpclient turns the MCP-server rows in the resource store (the
// agent.mcp table) into eino tools an agent can call.
//
// The flow is the one opsassist uses against its own configuration store: dial
// the server, let the mcp-go client perform the MCP handshake, have einomcp
// discover the tools, then wrap each of them so the model sees a legal function
// name and a plain-text result. What differs here is where the configuration
// lives — the resource store — and when
// discovery happens: tools are resolved lazily per run and cached for a TTL, so
// adding or editing an MCP server takes effect without restarting the service.
package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/onexstack/onexstack/pkg/store/where"

	"github.com/onexstack/onexai/internal/agent/model"
	"github.com/onexstack/onexai/internal/agent/store"
)

// Defaults applied when an Options field is left zero.
const (
	DefaultDiscoveryTimeout = 15 * time.Second
	DefaultTTL              = 5 * time.Minute
	// retryAfterFailure is how long a set that failed to build is remembered,
	// so a server that is down is not dialed again on every single run.
	retryAfterFailure = 30 * time.Second
	// closeGrace keeps a superseded connection alive long enough for a run that
	// captured the previous tool set to finish.
	closeGrace = time.Minute

	// settleTTL and settleWindow bound the staleness after a write that can
	// change a tool set. A binding/command change propagates through the
	// controller, the chat server's own 15s resolver cache and this manager; a
	// run that lands mid-propagation must not cache the still-old list for the
	// full TTL. For one minute after an invalidation, rebuilt entries expire
	// quickly so the next run probes again; after that the normal TTL resumes.
	settleTTL    = 15 * time.Second
	settleWindow = time.Minute
)

// Options configures a Manager.
type Options struct {
	// Enabled is the master switch. It has no usable zero value: callers must
	// set it explicitly (providers.go derives it from agent.mcp-enabled, whose
	// default is true).
	Enabled bool
	// DiscoveryTimeout bounds one server's handshake plus tool listing.
	DiscoveryTimeout time.Duration
	// TTL is how long a built tool set is served before it is rebuilt.
	TTL time.Duration
	// Reserved lists names MCP tools must not take, normally the built-in
	// tools, so a server cannot shadow current_time or ask_user_question.
	Reserved []string
	// Dialer is the connection seam. Nil dials over the network.
	Dialer Dialer
}

// mcpConn is the part of *client.Client a Manager needs in order to manage
// connection lifetimes. Narrowing it here keeps the test fake small.
type mcpConn interface {
	Close() error
}

// Dialer connects to one MCP server and discovers its tools. The returned
// connection belongs to the caller, which must Close it.
//
// agents are the agents the discovering run selected. They are stamped on the
// discovery calls, so a server that scopes its tools per agent answers with the
// right set; two dials of the same server for different agents may therefore
// discover different tools, which is why the cache is keyed by both.
type Dialer interface {
	Dial(ctx context.Context, m *model.MCPM, agents []string, discoveryTimeout time.Duration) (mcpConn, []tool.BaseTool, error)
}

// networkDialer is the production Dialer.
type networkDialer struct{}

func (networkDialer) Dial(ctx context.Context, m *model.MCPM, agents []string, timeout time.Duration) (mcpConn, []tool.BaseTool, error) {
	return connect(ctx, m, agents, timeout)
}

// Manager resolves MCP server names to eino tools and caches the result.
//
// A Manager is safe for concurrent use. Cached entries are keyed by the set of
// server names they were built from, so every agent asking for the same servers
// shares one connection pool and one tool set.
type Manager struct {
	rs     store.AIStore
	opts   Options
	dialer Dialer
	// dialCtx is the process-lifetime context that connections are bound to.
	// It must not be a per-run context: an SSE transport derives its stream
	// context from it and would die with the run that happened to dial first.
	dialCtx context.Context

	now        func() time.Time
	closeGrace time.Duration
	// guards remembers each tool's timeout streak across cache rebuilds, so a
	// server that keeps timing out stays skipped instead of being retried by
	// every new tool set. See breaker.go.
	guards *guardSet

	mu      sync.Mutex
	entries map[string]*entry
	// settleUntil is set by Invalidate*; entries rebuilt before it expires use
	// settleTTL instead of the configured TTL.
	settleUntil time.Time
}

// Invalidator drops cached tool sets. A write that can change what an agent
// sees — a command definition, a binding, an agent's resolved capabilities —
// calls it so the next run re-discovers instead of serving the old set.
type Invalidator interface {
	// Invalidate drops every cached set.
	Invalidate()
	// InvalidateAgent drops the sets scoped to name. One agent's status change
	// must not throw away every other agent's tool sets.
	InvalidateAgent(name string)
}

var _ Invalidator = (*Manager)(nil)

// entry is one cached tool set plus the connections serving it.
type entry struct {
	conns []mcpConn
	tools []tool.BaseTool
	// origins maps each model-facing tool name back to the MCP server that
	// published it. Authorization needs it to attribute a tool call to the
	// server whose MCPM.ResourceMappings (and rule rows) apply.
	origins map[string]string
	// agents is the normalised agent set this entry was built for, kept so
	// InvalidateAgent can target one agent's entries.
	agents []string
	exp    time.Time
	// loading marks a background rebuild in flight, making it single-flight so
	// a burst of runs past the TTL starts one dial rather than dozens.
	loading bool
}

// NewManager builds a Manager. ctx must be the process-lifetime context.
func NewManager(ctx context.Context, rs store.AIStore, opts Options) *Manager {
	if opts.DiscoveryTimeout <= 0 {
		opts.DiscoveryTimeout = DefaultDiscoveryTimeout
	}
	if opts.TTL <= 0 {
		opts.TTL = DefaultTTL
	}
	dialer := opts.Dialer
	if dialer == nil {
		dialer = networkDialer{}
	}
	m := &Manager{
		rs:         rs,
		opts:       opts,
		dialer:     dialer,
		dialCtx:    ctx,
		now:        time.Now,
		closeGrace: closeGrace,
		entries:    make(map[string]*entry),
	}
	// The guards read the clock through the Manager, so a test that replaces
	// m.now moves the breaker's cooldown as well as the cache's expiry.
	m.guards = newGuardSet(func() time.Time { return m.now() })
	return m
}

// Tools returns the MCP tools enabled by the given server names for a run that
// selected the given agents, dialing and caching them on first use. servers is
// normally the union of the selected agents' AgentM.MCPs, and agents is the
// selection itself: a server may publish a different tool set to different
// agents — the chat server narrows its tools to the commands the agents were
// granted — so the cache is keyed by both.
//
// An empty servers list yields no MCP tools: scope comes from the caller, and
// this manager deliberately has no "empty means everything" fallback. The
// built-in tools are not its business either — the caller composes those.
//
// The first call for a set blocks while its servers are dialed. After that the
// set is served from cache, and once its TTL lapses the previous set is served
// again while a single background rebuild runs, so a slow or dead server can
// never stall a run.
func (m *Manager) Tools(servers, agents []string) []tool.BaseTool {
	if m == nil || !m.opts.Enabled || m.rs == nil {
		return nil
	}
	agents = normalizeSet(agents)
	key, names := cacheKey(servers, agents)
	if key == "" {
		return nil
	}

	m.mu.Lock()
	if e, ok := m.entries[key]; ok {
		if !m.now().Before(e.exp) && !e.loading {
			e.loading = true
			go m.refresh(key, names, agents)
		}
		tools := e.tools
		m.mu.Unlock()
		return tools
	}
	m.mu.Unlock()

	return m.refresh(key, names, agents)
}

// Invalidate drops every cached tool set.
//
// The connections are retired after the usual grace period rather than closed
// immediately: a run that captured the previous set may still be using them.
func (m *Manager) Invalidate() {
	if m == nil {
		return
	}
	m.invalidate(func(*entry) bool { return true })
}

// InvalidateAgent drops the cached sets built for a run that selected name.
func (m *Manager) InvalidateAgent(name string) {
	if m == nil || name == "" {
		return
	}
	name = strings.TrimSpace(name)
	m.invalidate(func(e *entry) bool { return slices.Contains(e.agents, name) })
}

// invalidate removes the matching entries and opens a settle window during
// which rebuilt entries expire quickly; see settleTTL.
func (m *Manager) invalidate(match func(*entry) bool) {
	m.mu.Lock()
	var retired []mcpConn
	for key, e := range m.entries {
		if e != nil && match(e) {
			retired = append(retired, e.conns...)
			delete(m.entries, key)
		}
	}
	m.settleUntil = m.now().Add(settleWindow)
	m.mu.Unlock()

	m.retire(retired)
}

// InvokeTool calls one tool on one server, outside an agent run. It exists for
// custom commands of type "tool", which are a REST call rather than a turn and
// therefore have no run to borrow a tool set from.
//
// The tool name is the MCP wire name. The published model-facing name is a
// sanitised form of it; both are accepted so a command author can copy either.
func (m *Manager) InvokeTool(
	ctx context.Context, server, toolName, arguments string, agents []string,
) (string, error) {
	if m == nil || !m.opts.Enabled || m.rs == nil {
		return "", errors.New("MCP tools are not enabled on this deployment")
	}
	tools := m.Tools([]string{server}, agents)
	if len(tools) == 0 {
		return "", fmt.Errorf("MCP server %q is not reachable or exposes no tools", server)
	}
	want := SanitizeName(toolName)
	for _, base := range tools {
		info, err := base.Info(ctx)
		if err != nil || info == nil {
			continue
		}
		if info.Name != toolName && info.Name != want {
			continue
		}
		invokable, ok := base.(tool.InvokableTool)
		if !ok {
			continue
		}
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		return invokable.InvokableRun(ctx, arguments)
	}
	return "", fmt.Errorf("tool %q was not found on MCP server %q", toolName, server)
}

// Close releases every cached connection, stopping the transport goroutines
// they own. The server does not call it today — process exit reclaims the
// sockets — but it backs the tests and a future shutdown hook.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	entries := m.entries
	m.entries = make(map[string]*entry)
	m.mu.Unlock()

	var errs []error
	for _, e := range entries {
		errs = append(errs, closeConns(e.conns))
	}
	return errors.Join(errs...)
}

// refresh builds the tool set for names and installs it, returning whatever
// should now be served for that key.
//
// Failure policy: when no server answered, an existing set is kept and its
// expiry pushed out by retryAfterFailure instead of being replaced by nothing.
// A partial success (some servers answered) does replace the set — the missing
// server's tools would only fail if called anyway.
func (m *Manager) refresh(key string, names, agents []string) []tool.BaseTool {
	conns, tools, origins, ok := m.discover(names, agents)

	m.mu.Lock()
	defer m.mu.Unlock()

	prev := m.entries[key]
	if prev != nil {
		prev.loading = false
	}

	if !ok {
		if prev != nil {
			prev.exp = m.now().Add(retryAfterFailure)
			return prev.tools
		}
		m.entries[key] = &entry{agents: agents, exp: m.now().Add(retryAfterFailure)}
		return nil
	}

	now := m.now()
	exp := now.Add(m.opts.TTL)
	if now.Before(m.settleUntil) && settleTTL < m.opts.TTL {
		// A write just invalidated the cache. The source of truth may still be
		// catching up (controller status, chatserver reload, its resolver TTL),
		// so probe again soon instead of pinning this answer for the full TTL.
		exp = now.Add(settleTTL)
	}
	m.entries[key] = &entry{conns: conns, tools: tools, origins: origins, agents: agents, exp: exp}
	if prev != nil {
		m.retire(prev.conns)
	}
	return tools
}

// discover dials every named server concurrently and assembles their tools.
//
// It never returns an error: a server that is missing, disabled, unreachable or
// misconfigured is logged and skipped, so one bad row cannot break a run — or
// the startup path — for every other server. ok reports whether at least one
// server answered.
func (m *Manager) discover(names, agents []string) ([]mcpConn, []tool.BaseTool, map[string]string, bool) {
	// Index by (namespace, name). A bare name is only accepted when it is unique
	// across the cluster; once two namespaces hold the same name there is no
	// honest answer to "which one did the agent mean".
	byKey := make(map[string]*model.MCPM)
	byName := make(map[string]*model.MCPM)
	ambiguous := make(map[string]bool)
	_, mcps, _ := m.rs.MCPs().List(m.dialCtx, where.L(-1))
	for _, s := range mcps {
		if s == nil {
			continue
		}
		byKey[model.CanonicalRef(s.Namespace, s.Name)] = s
		if _, dup := byName[s.Name]; dup {
			ambiguous[s.Name] = true
		} else {
			byName[s.Name] = s
		}
	}

	type result struct {
		conns []mcpConn
		tools []tool.BaseTool
		ok    bool
	}
	results := make([]result, len(names))

	var wg sync.WaitGroup
	for i, name := range names {
		srv, known := byKey[name]
		if !known {
			// A legacy bare entry resolves only when the name is unambiguous.
			if ref := model.SplitRef(name); ref.Namespace == "" && !ambiguous[ref.Name] {
				srv, known = byName[ref.Name]
			}
		}
		if !known {
			slog.WarnContext(m.dialCtx, "mcp server is referenced by an agent but not in the resource store; skipping",
				"server", name)
			continue
		}
		if !srv.Enabled() {
			slog.WarnContext(m.dialCtx, "mcp server is disabled; skipping", "server", name)
			continue
		}
		wg.Add(1)
		go func(i int, srv *model.MCPM) {
			defer wg.Done()
			conn, tools, err := m.dialer.Dial(m.dialCtx, srv, agents, m.opts.DiscoveryTimeout)
			if err != nil {
				slog.WarnContext(m.dialCtx, "mcp server discovery failed; skipping",
					"server", srv.Name, "url", srv.URL, "err", err)
				return
			}
			slog.InfoContext(m.dialCtx, "mcp server discovered", "server", srv.Name, "tools", len(tools))
			results[i] = result{conns: []mcpConn{conn}, tools: tools, ok: true}
		}(i, srv)
	}
	wg.Wait()

	// Built-in names are off limits, so an MCP tool can never shadow one.
	taken := make(map[string]bool, len(m.opts.Reserved))
	for _, name := range m.opts.Reserved {
		taken[name] = true
	}

	var conns []mcpConn
	var out []tool.BaseTool
	origins := make(map[string]string)
	anyOK := false
	for i := range names {
		if !results[i].ok {
			continue
		}
		anyOK = true
		conns = append(conns, results[i].conns...)
		wrapped, serverOrigins := m.wrapTools(names[i], results[i].tools, taken)
		out = append(out, wrapped...)
		for name, server := range serverOrigins {
			origins[name] = server
		}
	}
	return conns, out, origins, anyOK
}

// discovered is one raw tool plus the wire name it was published under.
type discovered struct {
	inner tool.InvokableTool
	wire  string
}

// wrapTools turns one server's einomcp tools into model-facing tools, giving
// each a sanitised and unique name. It also returns the model-facing name of
// every published tool mapped back to this server, for authorization's tool ->
// MCP attribution.
func (m *Manager) wrapTools(server string, raw []tool.BaseTool, taken map[string]bool) ([]tool.BaseTool, map[string]string) {
	items := make([]discovered, 0, len(raw))
	for _, t := range raw {
		inner, ok := t.(tool.InvokableTool)
		if !ok {
			slog.WarnContext(m.dialCtx, "mcp tool cannot be invoked; skipping", "server", server)
			continue
		}
		info, err := inner.Info(m.dialCtx)
		if err != nil || info == nil {
			slog.WarnContext(m.dialCtx, "mcp tool has no usable descriptor; skipping",
				"server", server, "err", err)
			continue
		}
		items = append(items, discovered{inner: inner, wire: info.Name})
	}

	// A server need not list its tools in a stable order, and the collision
	// suffixes assigned below depend on position, so sort first: the same
	// configuration must always produce the same tool names.
	sort.Slice(items, func(i, j int) bool { return items[i].wire < items[j].wire })

	out := make([]tool.BaseTool, 0, len(items))
	origins := make(map[string]string, len(items))
	for _, item := range items {
		base := SanitizeName(item.wire)
		if base == "" {
			slog.WarnContext(m.dialCtx, "mcp tool name has no usable characters; skipping",
				"server", server, "tool", item.wire)
			continue
		}
		name := Uniquify(base, taken)
		if name != base {
			slog.WarnContext(m.dialCtx, "mcp tool name was taken; renamed",
				"server", server, "tool", item.wire, "name", name)
		}
		wrapped, err := newWireTool(m.dialCtx, item.inner, name)
		if err != nil {
			slog.WarnContext(m.dialCtx, "mcp tool descriptor is unusable; skipping",
				"server", server, "tool", item.wire, "err", err)
			continue
		}
		wrapped.guard = m.guards.get(server + "\x00" + name)
		out = append(out, toolutils.WrapToolWithErrorHandler(wrapped, m.toolErrorHandler(name)))
		origins[name] = server
	}
	return out, origins
}

// ToolOrigins returns, for the given server names and agents, the server each
// model-facing tool name was published by — the same discovery Tools serves,
// from the same cache. Authorization uses it to hand Coordinator the mcpName of
// a tool call, which decides both the tool-level rule match and which
// MCPM.ResourceMappings apply.
//
// The agents are part of the lookup because they are part of the cache key: a
// server may publish a different tool set to different agents, so the
// attribution must come from the entry the run's own discovery populated.
//
// An empty map means no MCP tools are in scope (disabled manager, no servers,
// or none reachable); callers must treat that as "no MCP attribution", not as
// an error.
func (m *Manager) ToolOrigins(servers, agents []string) map[string]string {
	if m == nil || !m.opts.Enabled || m.rs == nil {
		return nil
	}
	key, _ := cacheKey(servers, agents)
	if key == "" {
		return nil
	}

	// Populate the cache the same way Tools does, so a tool set that has never
	// been fetched still yields origins.
	m.Tools(servers, agents)

	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || len(e.origins) == 0 {
		return nil
	}
	out := make(map[string]string, len(e.origins))
	for k, v := range e.origins {
		out[k] = v
	}
	return out
}

// toolErrorHandler turns a tool failure into text the model can read and react
// to.
//
// This is load-bearing, not cosmetic. eino's ToolsNode aborts the entire run
// when a tool returns an error, and every failure path in the eino MCP adapter —
// a transport error, an MCP-level isError result, a malformed argument payload,
// a connection retired underneath a live run — surfaces as one. Without this
// wrapper a single flaky tools/call reaches the user as a failed run.
func (m *Manager) toolErrorHandler(name string) toolutils.ErrorHandler {
	return func(ctx context.Context, err error) string {
		slog.WarnContext(ctx, "mcp tool call failed", "tool", name, "err", err)
		return fmt.Sprintf("tool %s failed: %v", name, err)
	}
}

// retire closes superseded connections after a grace period. Nothing tracks
// which tools a live run has captured, so closing immediately could pull a
// connection out from under it; the delay covers a run that started just before
// the rebuild. opsassist never closed these at all and leaked one per rebuild.
func (m *Manager) retire(conns []mcpConn) {
	if len(conns) == 0 {
		return
	}
	if m.closeGrace <= 0 {
		_ = closeConns(conns)
		return
	}
	time.AfterFunc(m.closeGrace, func() { _ = closeConns(conns) })
}

// closeConns closes every connection, joining the errors.
func closeConns(conns []mcpConn) error {
	var errs []error
	for _, c := range conns {
		if c == nil {
			continue
		}
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// cacheKey turns a requested server set and agent set into a stable key and the
// normalised server names behind it. Trimming, deduplicating and sorting means
// any ordering or repetition of the same names shares one cache entry.
//
// The agents are part of the key because a server may publish a different tool
// set to different agents; two runs over the same servers but different agents
// must not share an entry. An empty agent set — the standalone form, no agent
// scoping — keys distinctly from any named set, so it never borrows a scoped
// entry either way around.
func cacheKey(servers, agents []string) (string, []string) {
	names := normalizeSet(servers)
	if len(names) == 0 {
		return "", nil
	}
	return strings.Join(names, "\x00") + "\x01" + strings.Join(normalizeSet(agents), "\x00"), names
}

// normalizeSet trims, drops empties and duplicates, and sorts, so any ordering
// or repetition of the same names yields one form.
func normalizeSet(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
