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

// Package mcpserver exposes the oai-chat-apiserver command tree as Model Context
// Protocol tools for oai-agent-apiserver's agents.
//
// The bridge itself is github.com/feizaonet/cobrax, the same library the
// onexai CLI uses for its own `onexai mcp` server. This package supplies the
// three things cobrax cannot know on its own: which commands are tools, what a
// command's output means to a model, and who is calling.
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/feizaonet/cobrax"
	"github.com/mark3labs/mcp-go/mcp"
	mcpsdk "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/agentcmd"
	"github.com/onexstack/onexai/internal/chat/pkg/command"
	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
	"github.com/onexstack/onexai/pkg/options"
)

// toolTimeout bounds a single tool call.
//
// It exists because cobrax's in-process execution holds a process-wide lock for
// the whole call (the stdio redirection it performs rewrites the process's file
// descriptors 1 and 2, so two calls cannot overlap) and already detaches the
// command from the caller's cancellation. Without a deadline of its own, one
// wedged command — an API call that never returns — would block every other
// tool call for the life of the process.
//
// Two minutes is generous for the commands exposed here (a Lark or Zadig round
// trip, a list query) and short enough that a stuck call is noticed.
const toolTimeout = 2 * time.Minute

// commandLister reads the custom-command catalog. It is a field on Bridge
// rather than a package function so a test can move the catalog without a fake
// clientset.
type commandLister func(context.Context) ([]runtime.CustomCommand, error)

// Bridge publishes the command tree over MCP and keeps the published custom
// tools in step with the CustomCommand catalog.
//
// cobrax derives its tool schemas once, from the first command tree it is
// handed, so a command added after that point has no tool. The bridge therefore
// owns the whole cobrax server rather than just the command tree: when the
// catalog changes it builds a new server from the new tree, installs the new
// streamable-HTTP handler in the transport, and swaps it in atomically. The
// transport is stateless, so an in-flight request finishes on the server it
// started with and no session state is lost.
type Bridge struct {
	opts     *options.MCPOptions
	sr       *runtime.StaticRuntime
	list     commandLister
	interval time.Duration
	tr       *transport

	state   atomic.Pointer[serverState]
	reloads atomic.Int64

	// mu serialises reloads and guards failed, the flag behind the
	// warn-once/degrade-to-debug logging of a listing error.
	mu     sync.Mutex
	failed bool
}

// serverState is one complete MCP server plus the catalog it was built from.
type serverState struct {
	server      *cobrax.MCPServer
	customs     []runtime.CustomCommand
	customTools map[string]string
}

// New builds the MCP listener that publishes this service's commands.
//
// It returns (nil, nil) when the listener is not enabled. That is not an error
// and callers are expected to treat a nil server as "nothing to start": an
// unconfigured MCP listener is the state every deployment is in until an
// operator asks for one.
func New(opts *options.MCPOptions, sr *runtime.StaticRuntime) (*Bridge, error) {
	if opts == nil || !opts.Enabled {
		return nil, nil
	}
	if sr == nil {
		return nil, fmt.Errorf("mcpserver: static runtime is required")
	}

	b := &Bridge{
		opts:     opts,
		sr:       sr,
		list:     func(ctx context.Context) ([]runtime.CustomCommand, error) { return listCustomCommands(ctx, sr) },
		interval: opts.RefreshInterval,
		tr:       newTransport(opts, sr),
	}
	if b.interval <= 0 {
		b.interval = options.DefaultMCPRefreshInterval
	}

	// The first read is allowed to fail: the listener is an optional side
	// channel and a slow or unavailable agent service must not keep it from
	// starting. The next tick picks the catalog up.
	ctx, cancel := context.WithTimeout(context.Background(), customCommandListTimeout)
	customs, err := b.list(ctx)
	cancel()
	if err != nil {
		slog.Warn("MCP bridge cannot list custom commands; starting without them", "error", err)
		b.failed = true
	}

	state, err := b.build(canonicalCustoms(customs))
	if err != nil {
		return nil, err
	}
	b.tr.setServer(state.server.RawMCPServer())
	b.state.Store(state)
	return b, nil
}

// Start begins serving the MCP listener and refreshing its custom tools.
//
// It blocks until ctx is cancelled. Only the first server is started; a reload
// swaps the transport's handler rather than starting a second listener.
func (b *Bridge) Start(ctx context.Context) error {
	if b == nil {
		return nil
	}
	go b.run(ctx)

	state := b.state.Load()
	if state == nil {
		return fmt.Errorf("mcpserver: bridge has no server")
	}
	return state.server.Start(ctx)
}

// Tools returns the tools of the server currently being served.
func (b *Bridge) Tools() []*mcp.Tool {
	if b == nil {
		return nil
	}
	state := b.state.Load()
	if state == nil {
		return nil
	}
	return state.server.Tools()
}

// run refreshes immediately and then once per interval until ctx is cancelled.
func (b *Bridge) run(ctx context.Context) {
	b.reload()

	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.reload()
		}
	}
}

// reload re-reads the catalog and, when it changed, publishes a new server.
func (b *Bridge) reload() {
	b.mu.Lock()
	defer b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), customCommandListTimeout)
	customs, err := b.list(ctx)
	cancel()
	if err != nil {
		// Never publish an empty catalog over a listing failure: an outage of
		// the agent service would otherwise silently withdraw every custom tool.
		if !b.failed {
			slog.Warn("MCP bridge cannot list custom commands; keeping the current tool set", "error", err)
			b.failed = true
		} else {
			slog.Debug("MCP bridge still cannot list custom commands", "error", err)
		}
		return
	}
	if b.failed {
		slog.Info("MCP bridge recovered its custom command listing")
		b.failed = false
	}

	customs = canonicalCustoms(customs)
	prev := b.state.Load()
	if prev != nil && reflect.DeepEqual(prev.customs, customs) {
		return
	}

	state, err := b.build(customs)
	if err != nil {
		slog.Error("MCP bridge cannot rebuild its tool set; keeping the current one", "error", err)
		return
	}

	// The handler is installed before the state is visible, so a request that
	// observes the new state always finds the matching handler.
	b.tr.setServer(state.server.RawMCPServer())
	b.state.Store(state)
	b.reloads.Add(1)

	added, removed := diffCustomTools(prev, state)
	slog.Info("MCP bridge reloaded custom tools",
		"added", added, "removed", removed, "total", len(state.customTools))
}

// build assembles one cobrax server from customs.
func (b *Bridge) build(customs []runtime.CustomCommand) (*serverState, error) {
	source := &commandSource{sr: b.sr, customs: customs}
	srv, err := cobrax.NewMCPServer(
		// cobrax has its own MCPOptions with a narrower field set; copy across
		// rather than sharing the type, so a field added to our options cannot
		// silently acquire a different meaning inside the bridge.
		cobrax.MCPOptions{
			Enabled: true,
			Addr:    b.opts.Addr,
			Name:    b.opts.Name,
			Version: b.opts.Version,
		},
		source.newTree,
		cobrax.WithSelectors(selector()),
		cobrax.WithTransport(b.tr),
	)
	if err != nil {
		return nil, fmt.Errorf("build MCP server: %w", err)
	}

	// The map is taken from the tree cobrax actually registered, not from the
	// requested catalog: a command whose name collides with a built-in is
	// dropped from the tree, and mapping it anyway would make the filter treat
	// the built-in tool as a custom one.
	customTools := registeredCustomTools(source.schemaTree())
	attachToolFilter(srv, b.sr, customTools)

	return &serverState{server: srv, customs: customs, customTools: customTools}, nil
}

// commandSource builds fresh command trees for cobrax and remembers the first
// one, which is the tree cobrax derived its tool schemas from.
type commandSource struct {
	sr      *runtime.StaticRuntime
	customs []runtime.CustomCommand

	mu     sync.Mutex
	schema *cobra.Command
}

func (c *commandSource) newTree() *cobra.Command {
	// root is captured by the responder's resolver below, so the responder
	// writes into whichever tree cobrax is about to redirect.
	var root *cobra.Command
	out := newResponder(func() *cobra.Command { return root })

	// The whole catalog, not a menu: this tree decides which tools exist, and
	// cobrax works out its tool paths once, from the tree it is handed here. A
	// tree filtered by grants — and this one is built without a caller, so every
	// grant list would be empty — registers no tool at all, leaving even a
	// caller that declares its agents with nothing to call. What a caller may
	// run is narrowed per request instead: the tool list by toolFilter, the call
	// itself by ValidateGranted.
	//
	// It is built on a background context carrying the MCP surface marker. That
	// context is used for one thing only — HideCommands, which hides commands a
	// surface cannot run — and MCP is a surface that can render a prompt
	// command's text even though it cannot draw a turn. The per-call identity is
	// injected at the transport and reaches the command through cobra's own
	// ExecuteContext instead; see transport.go.
	root = command.NewMCPCommandTree(context.Background(), c.sr, out, c.customs)

	c.mu.Lock()
	if c.schema == nil {
		c.schema = root
	}
	c.mu.Unlock()
	return root
}

func (c *commandSource) schemaTree() *cobra.Command {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schema
}

// attachToolFilter installs the per-caller tool filter on srv.
//
// The tool list is filtered by the agents the caller runs as, so an agent bound
// to this server sees only the tools of the commands it was granted — the same
// set its slash-command menu offers. The tool-to-command mapping is resolved once
// here, from the tools the bridge just registered, so the per-request filter is a
// lookup rather than a parse. It is a function of its own so a test can install
// the same filter over its own tree instead of reaching into the server.
func attachToolFilter(srv *cobrax.MCPServer, sr *runtime.StaticRuntime, customTools map[string]string) {
	mcpsdk.WithToolFilter(toolFilter(sr, toolTops(srv, customTools)))(srv.RawMCPServer())
}

// toolTops maps each registered tool to the catalogued command that backs it.
//
// It is built once per published server. A tool no command backs — the root's
// own additions, the MCP-only askai entry among them — keeps no entry and is
// left in the list unfiltered, matching ValidateGranted's treatment of exactly
// those. They are logged so an operator can see what the grant rules cannot
// reach, and they are what a caller with no grants still sees: askai-mcp is the
// one tool that outlives an empty grant list.
func toolTops(srv *cobrax.MCPServer, customTools map[string]string) map[string]string {
	tools := srv.Tools()
	tops := make(map[string]string, len(tools))
	var ungated []string
	for _, t := range tools {
		// A custom tool is gated by the binding that exposed it, not by a
		// catalog grant. The prefix keeps the two kinds apart in one map.
		if name, ok := customTools[t.Name]; ok {
			tops[t.Name] = customToolPrefix + name
			continue
		}
		top, ok := command.TopLevelCommand(t.Name)
		if !ok {
			ungated = append(ungated, t.Name)
			continue
		}
		tops[t.Name] = top
	}
	if len(ungated) > 0 {
		slog.Info("MCP tools with no catalogued command are never filtered by agent grants",
			"tools", ungated)
	}
	return tops
}

// toolFilter narrows a tools/list to the tools of the commands the calling
// agents were granted.
//
// It resolves the same CommandSelection the command tree does (see
// attributeMCPCaller for where the agents come from), so the tools a model is
// shown and the commands it may run cannot disagree. A caller whose agents no
// rule covers has been granted nothing, so the list it is shown is the tools
// that no grant can reach either — see toolTops for what those are.
func toolFilter(sr *runtime.StaticRuntime, tops map[string]string) mcpsdk.ToolFilterFunc {
	return func(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
		sel := command.Selection(ctx, sr)

		kept := make([]mcp.Tool, 0, len(tools))
		for _, t := range tools {
			top := tops[t.Name]
			switch {
			case top == "":
				kept = append(kept, t)
			case strings.HasPrefix(top, customToolPrefix):
				if sel.AllowsCustom(strings.TrimPrefix(top, customToolPrefix)) {
					kept = append(kept, t)
				}
			case sel.Allows(top):
				kept = append(kept, t)
			}
		}
		return kept
	}
}

const (
	// customToolPrefix marks a tool-to-command entry that names a custom
	// command rather than a catalogued one. Catalogued command names cannot
	// contain a colon, so the two kinds cannot collide.
	customToolPrefix = "custom:"

	// customCommandListTimeout bounds one read of the command list. A slow or
	// unavailable agent service must not delay the MCP listener or pile up
	// reloads: the bridge keeps serving the set it already has and says so.
	customCommandListTimeout = 5 * time.Second
)

// listCustomCommands reads every command definition from the agent service.
//
// The catalog is re-read by Bridge.reload, so a command added after startup
// becomes a tool within one refresh interval. The per-caller filter still
// decides whether a given caller sees it.
func listCustomCommands(ctx context.Context, sr *runtime.StaticRuntime) ([]runtime.CustomCommand, error) {
	if sr == nil || sr.Clientset == nil || sr.Clientset.Agent() == nil {
		return nil, nil
	}
	commands, err := sr.Clientset.Agent().ListCustomCommand(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]runtime.CustomCommand, 0, len(commands))
	for _, c := range commands {
		if c.GetName() == "" || !c.GetEnabled() {
			continue
		}
		out = append(out, agentcmd.CustomCommandFromProto(c))
	}
	return out, nil
}

// canonicalCustoms gives the catalog a stable order so two reads of the same
// set compare equal and the reload skips rebuilding.
func canonicalCustoms(customs []runtime.CustomCommand) []runtime.CustomCommand {
	out := append([]runtime.CustomCommand(nil), customs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		// Two namespaces may hold the same command name. The tree keeps the
		// first, so the order here decides which one that is; name alone would
		// leave it to the list endpoint's iteration order.
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		return out[i].Usage < out[j].Usage
	})
	for i := range out {
		aliases := append([]string(nil), out[i].Aliases...)
		sort.Strings(aliases)
		out[i].Aliases = aliases
	}
	return out
}

// registeredCustomTools maps each tool registered from a custom command onto
// the command's canonical name.
//
// It walks the tree cobrax actually registered rather than the requested
// catalog, so a command dropped for colliding with a built-in (or an alias that
// never became a tool) cannot misclassify the built-in tool of the same name.
func registeredCustomTools(root *cobra.Command) map[string]string {
	if root == nil {
		return nil
	}
	out := make(map[string]string)
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		// Mirror cobrax's own registration filter: a hidden, deprecated or
		// unrunnable command never becomes a tool, so mapping it here would make
		// the filter gate a tool that does not exist.
		registerable := !cmd.Hidden && cmd.Deprecated == "" &&
			(cmd.Run != nil || cmd.RunE != nil || cmd.PreRun != nil || cmd.PreRunE != nil)
		if name := cmd.Annotations[chat.CustomCommand]; registerable && name != "" {
			out[toolNameFor(root, cmd)] = name
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return out
}

// toolNameFor reproduces cobrax's empty-prefix tool naming: the command path
// without the root's name, with spaces replaced by underscores.
func toolNameFor(root, cmd *cobra.Command) string {
	path := cmd.CommandPath()
	if rest, ok := strings.CutPrefix(path, root.Name()+" "); ok {
		path = rest
	}
	return strings.ReplaceAll(path, " ", "_")
}

// diffCustomTools reports the tool names added and removed between two states.
func diffCustomTools(prev *serverState, next *serverState) (added, removed []string) {
	var before map[string]string
	if prev != nil {
		before = prev.customTools
	}
	for name := range next.customTools {
		if _, ok := before[name]; !ok {
			added = append(added, name)
		}
	}
	for name := range before {
		if _, ok := next.customTools[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// selector declares which commands become tools.
//
// The whitelist is mandatory, not a refinement: cobrax's default is to include
// every command it did not find an explicit opt-out on, which on this tree
// would publish the whole command set — including the admin-gated commands that
// switch the AI backend or decide which directory a local process runs in. The
// annotation is the same one the chat surfaces already use to mark a command as
// MCP-worthy, so a command opts in once and both features agree.
func selector() cobrax.Selector {
	return cobrax.Selector{
		CmdSelector: func(cmd *cobra.Command) bool {
			return cmd.Annotations[chat.MCPTool] == chat.True
		},

		// The tree inherits the process's global flag set, which the k8s client
		// libraries and klog register on (command.NewBotCommandTree calls
		// AddGoFlagSet(flag.CommandLine)). cobrax drops help, hidden and
		// deprecated flags but knows nothing about klog's, so without this every
		// tool's schema would advertise --v, --logtostderr and friends as
		// parameters a model may set — logging flags it has no business touching
		// and cannot use anyway, since they were parsed before the call arrived.
		InheritedFlagSelector: cobrax.NoFlags,

		Middleware: toolMiddleware,
	}
}

// toolMiddleware shapes what a tool call returns.
//
// It does not read the error runInProcess reports, because there is none to
// read: cobrax captures the command's failure into ToolOutput and returns a nil
// error, exactly as the REST and MCP surfaces of this service treat the
// responder's contents as the reply and drop the returned error. A command that
// wrote something is answering the caller.
func toolMiddleware(ctx context.Context, req mcp.CallToolRequest, in cobrax.ToolInput, next cobrax.ExecuteFunc) (*mcp.CallToolResult, cobrax.ToolOutput, error) {
	dr := runtime.From(ctx)
	dr.Source = runtime.RequestSourceMCP

	// Detach from the request's cancellation while keeping its values (the
	// identity among them), then bound the call. context.WithoutCancel alone
	// would mean a caller that hung up left the command running; a bare
	// WithTimeout would cancel the command the moment the client disconnected.
	isolated, cancel := context.WithTimeout(context.WithoutCancel(ctx), toolTimeout)
	defer cancel()

	result, out, err := next(runtime.Inject(isolated, dr), req, in)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), out, nil
	}
	if result != nil {
		return result, out, nil
	}

	text := strings.TrimSpace(out.StdOut)
	switch {
	case text != "" && out.ExitCode != 0:
		// The command rendered its own failure. Sending it as a tool error keeps
		// the model from reading a refusal as a result, while still handing it
		// the detail the command took the trouble to write (pipeline, for one,
		// echoes the parameters it would have sent).
		return mcp.NewToolResultError(text), out, nil
	case text != "":
		return mcp.NewToolResultText(text), out, nil
	case out.ExitCode != 0:
		// Nothing on stdout, so the message only exists on stderr. This is the
		// one path that reads stderr, and it reads it because the alternative is
		// a tool error with no message at all. It can carry log noise: cobrax
		// redirects the whole process's descriptors while the command runs, so a
		// concurrent log line lands here too. On a failure that is a price worth
		// paying; on a success it never happens, which is why the success path
		// ignores stderr entirely.
		return mcp.NewToolResultError(strings.TrimSpace(out.StdErr)), out, nil
	default:
		return mcp.NewToolResultText("（命令未产生输出）"), out, nil
	}
}
