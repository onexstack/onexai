package cobrax

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	einojsonschema "github.com/eino-contrib/jsonschema"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

// inProcessMu serialises in-process tool execution. The fd-level stdio
// redirection performed by redirectStdio rewrites the process-global file
// descriptors 1 and 2 (stdout/stderr), so two concurrent in-process calls would
// otherwise write into each other's capture pipes. The subprocess execution
// model does not share this limitation and is unaffected by this lock.
var inProcessMu sync.Mutex

// MCPOptions holds configuration for the MCP (Model Context Protocol) server.
// It is used with NewMCPServer to control how the server advertises itself and
// which network address it listens on.
type MCPOptions struct {
	// Enabled controls whether the MCP server is started.
	// When false, Start returns immediately without binding any port.
	Enabled bool `json:"enabled" mapstructure:"enabled"`

	// Addr is the listen address for the MCP server (e.g. ":8090").
	// Required when Enabled is true.
	Addr string `json:"addr" mapstructure:"addr"`

	// Name is the MCP server name advertised to clients.
	Name string `json:"name" mapstructure:"name"`

	// Version is the MCP server version advertised to clients.
	Version string `json:"version" mapstructure:"version"`

	// BaseURL is the publicly reachable base URL for the SSE server
	// (e.g. "http://localhost:8090"). Used to construct the SSE endpoint
	// advertised to clients. If empty, it is derived from Addr.
	BaseURL string `json:"baseURL" mapstructure:"baseURL"`

	// Subprocess controls whether tools are executed in a separate OS process.
	// When true, each tool invocation re-invokes the current binary as a
	// subprocess via execSubprocess. The subprocess survives MCP server
	// termination, so long-running commands (e.g. project scaffolding) are
	// not killed when the MCP client times out and closes the transport.
	// Default is false (in-process execution via runInProcess).
	Subprocess bool `json:"subprocess" mapstructure:"subprocess"`
}

// MCPServer is an MCP server that exposes Cobra commands as MCP tools.
//
// Each tool invocation calls cmdFactory to produce a fresh *cobra.Command tree,
// which means every call gets its own Options structs, flag values, ctx fields,
// and closure-captured variables. No shared mutable state exists between
// concurrent or sequential tool calls, so no mutex or reset logic is required.
//
// Example:
//
//	biz := newBotSreBiz(client, clientset, ...)
//
//	factory := func() *cobra.Command {
//	    root := &cobra.Command{Use: "myapp"}
//	    root.AddCommand(sre.NewOpenCmd(biz, ioStreams))
//	    return root
//	}
//
//	srv, err := cobrax.NewMCPServer(cobrax.MCPOptions{
//	    Enabled: true,
//	    Addr:    ":8090",
//	    Name:    "myapp",
//	    Version: "1.0.0",
//	}, factory)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if err := srv.Start(ctx); err != nil {
//	    log.Fatal(err)
//	}
type MCPServer struct {
	opts   MCPOptions
	server *mcpserver.MCPServer
	// cmdFactory is called once per tool invocation to produce a fresh
	// *cobra.Command tree. This eliminates all shared-state hazards:
	// each call gets new Options structs, flag values, ctx, RunE/Run
	// function pointers, and all other closure-captured variables.
	cmdFactory func() *cobra.Command
	tools      []*mcp.Tool
	// selectors holds the set of selector rules used during tool registration.
	// Defaults to a single catch-all Selector{} when none are configured.
	selectors []Selector
	// toolMetas maps tool name → per-tool metadata (flag names + arg specs)
	// computed at registration time. Used during in-process execution to split
	// the flat MCP input back into flags and positional arguments.
	toolMetas map[string]toolMeta
	// toolPaths maps tool name → the cobra sub-command path segments to pass
	// to the fresh root command produced by cmdFactory. Stored at registration
	// time so runInProcess never needs to reverse-engineer the path from the
	// tool name, which breaks when the root command name contains characters
	// that are illegal in MCP tool names (e.g. "/").
	toolPaths map[string][]string
	// transport serves the MCP server over a wire protocol. When nil, Start
	// defaults to SSETransport. It can be customised with WithTransport.
	transport Transport
}

// ServerOption is a functional option for configuring the MCPServer.
type ServerOption func(*MCPServer)

// WithSelectors sets the selector rules used during tool registration.
// If provided, it overrides the default catch-all Selector{}.
func WithSelectors(selectors ...Selector) ServerOption {
	return func(s *MCPServer) {
		if len(selectors) > 0 {
			s.selectors = selectors
		}
	}
}

// WithTransport sets the transport used by Start to serve the MCP server. When
// nil, Start defaults to SSETransport. Custom transports (e.g. a streamable-HTTP
// transport) can be supplied to change the wire protocol.
func WithTransport(t Transport) ServerOption {
	return func(s *MCPServer) {
		s.transport = t
	}
}

// NewMCPServer constructs an MCPServer that exposes Cobra commands as MCP tools.
//
// cmdFactory is called once immediately to obtain a reference command tree for
// tool schema registration (a read-only traversal). It is then called once per
// tool invocation at runtime to produce a fresh *cobra.Command tree, ensuring
// that every call gets its own Options structs, flag values, ctx fields, and
// closure-captured variables with no shared mutable state between calls.
//
// cmdFactory must not be nil and must return a non-nil *cobra.Command each time
// it is called; NewMCPServer returns an error if either condition is violated.
//
// opts.Enabled must be true for Start to actually bind a port; if false, Start
// is a no-op, which lets callers gate the MCP server behind a feature flag.
func NewMCPServer(opts MCPOptions, cmdFactory func() *cobra.Command, serverOpts ...ServerOption) (*MCPServer, error) {
	if cmdFactory == nil {
		return nil, fmt.Errorf("cobrax.NewMCPServer: cmdFactory must not be nil")
	}

	// Call the factory once to obtain a reference tree for schema registration.
	// This is a read-only traversal; the resulting tree is never executed.
	schemaCmd := cmdFactory()
	if schemaCmd == nil {
		return nil, fmt.Errorf("cobrax.NewMCPServer: cmdFactory returned nil")
	}

	// Resolve server name: prefer opts.Name, fall back to root command name.
	name := opts.Name
	if name == "" {
		name = schemaCmd.Name()
	}

	mcpSrv := mcpserver.NewMCPServer(name, opts.Version)

	srv := &MCPServer{
		opts:       opts,
		server:     mcpSrv,
		cmdFactory: cmdFactory,
		selectors:  []Selector{{}}, // default catch-all selector
		toolMetas:  make(map[string]toolMeta),
		toolPaths:  make(map[string][]string),
	}

	for _, opt := range serverOpts {
		opt(srv)
	}

	// Register all commands from the reference tree as MCP tools.
	// Only schema metadata (tool names, descriptions, flag specs) is read here;
	// the reference tree is never executed.
	srv.registerToolsRecursive(schemaCmd)

	return srv, nil
}

// Tools returns the list of MCP tools registered on this server.
// The slice is ordered leaf-first (depth-first traversal order).
// It can be used for introspection, testing, or generating tool manifests.
func (s *MCPServer) Tools() []*mcp.Tool {
	return s.tools
}

// EinoTools returns the registered MCP tools as a slice of
// github.com/cloudwego/eino/components/tool.BaseTool.
//
// Each tool's input schema (held in RawInputSchema) is deserialised into an
// eino-compatible *jsonschema.Schema. The returned tools implement BaseTool
// (Info method only) and can be passed directly to an eino ChatModel via
// WithTools / BindTools.
func (s *MCPServer) EinoTools() ([]einotool.BaseTool, error) {
	result := make([]einotool.BaseTool, 0, len(s.tools))
	for _, t := range s.tools {
		// createToolFromCmd populates RawInputSchema (not the structured
		// InputSchema), so deserialise the raw JSON schema directly. RawInputSchema
		// is already a json.RawMessage, so no re-marshalling is needed.
		if len(t.RawInputSchema) == 0 {
			return nil, fmt.Errorf("cobrax: tool %q has no input schema", t.Name)
		}
		einoSchema := &einojsonschema.Schema{}
		if err := json.Unmarshal(t.RawInputSchema, einoSchema); err != nil {
			return nil, fmt.Errorf("cobrax: unmarshal input schema for tool %q: %w", t.Name, err)
		}
		result = append(result, &einoToolWrapper{
			info: &schema.ToolInfo{
				Name:        t.Name,
				Desc:        t.Description,
				ParamsOneOf: schema.NewParamsOneOfByJSONSchema(einoSchema),
			},
		})
	}
	return result, nil
}

// einoToolWrapper wraps a schema.ToolInfo and implements einotool.BaseTool.
type einoToolWrapper struct {
	info *schema.ToolInfo
}

// Info implements einotool.BaseTool.
func (w *einoToolWrapper) Info(_ context.Context) (*schema.ToolInfo, error) {
	return w.info, nil
}

// RawMCPServer returns the underlying mcpserver.MCPServer instance.
func (s *MCPServer) RawMCPServer() *mcpserver.MCPServer {
	return s.server
}

// Start begins serving the MCP server over SSE on opts.Addr.
//
// It blocks until the context is cancelled or an OS signal (SIGINT/SIGTERM) is
// received, at which point it performs a graceful shutdown. If opts.Enabled is
// false, Start returns nil immediately.
func (s *MCPServer) Start(ctx context.Context) error {
	if !s.opts.Enabled {
		slog.Info("MCP server is disabled, skipping start")
		return nil
	}

	if s.opts.Addr == "" {
		return fmt.Errorf("MCPOptions.Addr must not be empty when Enabled is true")
	}

	t := s.transport
	if t == nil {
		t = SSETransport{}
	}

	return t.Serve(ctx, s.server, ServeOptions{
		Addr:    s.opts.Addr,
		BaseURL: s.opts.BaseURL,
	})
}

// shutdownSignal returns a channel that is closed when either the context is
// cancelled or an interrupt signal (SIGINT/SIGTERM) is received. It is used by
// the transport servers to trigger graceful shutdown, including the legacy
// serveHTTP/serveREST paths that are launched via rootCmd.Execute() where
// cmd.Context() is a never-cancelled context.Background().
func shutdownSignal(ctx context.Context) <-chan struct{} {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
		case <-ctx.Done():
		}
		signal.Stop(ch)
		close(done)
	}()
	return done
}

// registerToolsRecursive walks the command tree rooted at cmd and registers
// every eligible command as an MCP tool on s.server.
func (s *MCPServer) registerToolsRecursive(cmd *cobra.Command) {
	// Recurse into sub-commands first so the tool list is ordered leaf-first.
	for _, sub := range cmd.Commands() {
		s.registerToolsRecursive(sub)
	}

	// Apply built-in safety filters.
	if s.cmdFilter(cmd) {
		return
	}

	// Build the cobra sub-command path for this command: the full CommandPath()
	// minus the root command name. Stored in toolPaths so that runInProcess can
	// pass the exact segments to the fresh root command without having to
	// reverse-engineer them from the tool name (which would break when the root
	// command name contains characters illegal in MCP tool names, e.g. "/").
	cmdPath := cmdSubPath(cmd)

	// Use an empty tool name prefix so the tool name is derived purely from the
	// sub-command path. The root command name is intentionally excluded because
	// it may contain characters that are illegal in MCP tool names (e.g. "/").
	toolNamePrefix := ""

	// Evaluate selectors in order; the first matching selector wins.
	for i, sel := range s.selectors {
		if sel.CmdSelector != nil && !sel.CmdSelector(cmd) {
			continue
		}

		// Create the MCP tool definition (flat schema, name, description) and
		// the per-tool metadata (flag names + arg specs).
		tool, meta := sel.createToolFromCmd(cmd, toolNamePrefix)
		slog.Debug("registered in-process tool", "tool_name", tool.Name, "selector_index", i)

		// Store the metadata keyed by tool name for use at call time.
		s.toolMetas[tool.Name] = meta

		// Store the cobra command path segments for this tool.
		s.toolPaths[tool.Name] = cmdPath

		// Capture sel and meta in a closure for the tool handler.
		toolMeta := meta

		// Choose execution model: subprocess survives MCP server termination
		// (useful for long-running commands like project scaffolding);
		// in-process is faster for short commands (version, color, etc.).
		var handler func(context.Context, mcp.CallToolRequest, ToolInput) (*mcp.CallToolResult, ToolOutput, error)
		if s.opts.Subprocess {
			handler = s.makeSubprocessHandler(sel, cmd)
		} else {
			handler = s.makeInProcessHandler(sel, cmd)
		}
		s.server.AddTool(*tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			input := decodeToolInput(req, toolMeta)
			result, output, err := handler(ctx, req, input)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if result != nil {
				return result, nil
			}
			return toolOutputToResult(output), nil
		})

		s.tools = append(s.tools, tool)

		// Only the first matching selector is used.
		break
	}
}

// cmdFilter returns true when cmd should be excluded from tool registration.
func (s *MCPServer) cmdFilter(cmd *cobra.Command) bool {
	return basicCmdFilter(cmd, "help", "completion")
}

// basicCmdFilter applies the shared safety filters used by both the legacy
// Config API and the in-process MCPServer API.
//
// Commands are excluded when they are explicitly disabled via the
// AnnotationMCPTool annotation, hidden, deprecated, lack a runnable function,
// or belong to one of the excludeNames groups (e.g. "help", "completion", or
// the cobrax command name itself).
func basicCmdFilter(cmd *cobra.Command, excludeNames ...string) bool {
	// Check AnnotationMCPTool: if explicitly "false", exclude this command
	// from MCP tool registration entirely, before any selector evaluation.
	if v, ok := cmd.Annotations[AnnotationMCPTool]; ok {
		if enabled, err := strconv.ParseBool(v); err != nil {
			slog.Warn("invalid bool value for annotation, skipping",
				"key", AnnotationMCPTool, "value", v)
		} else if !enabled {
			return true
		}
	}

	if cmd.Hidden || cmd.Deprecated != "" {
		return true
	}

	if cmd.Run == nil && cmd.RunE == nil && cmd.PreRun == nil && cmd.PreRunE == nil {
		return true
	}

	return AllowCmdsContaining(excludeNames...)(cmd)
}

// makeInProcessHandler returns a tool handler function that executes a freshly
// created command tree in-process rather than as a subprocess.
//
// The handler calls s.cmdFactory() on every invocation to obtain a new
// *cobra.Command tree. This guarantees that:
//   - All Options structs are freshly allocated (no flag bleed between calls).
//   - The cobra ctx field starts as nil so ExecuteContext propagates correctly.
//   - PersistentPreRunE mutations (e.g. --mock=success overwriting RunE/Run)
//     are confined to the single invocation's tree and never affect other calls.
//   - warningHandler and other closure-captured variables are brand new each time.
//
// Because no shared mutable state exists, no mutex and no reset logic is needed.
func (s *MCPServer) makeInProcessHandler(sel Selector, _ *cobra.Command) func(context.Context, mcp.CallToolRequest, ToolInput) (*mcp.CallToolResult, ToolOutput, error) {
	return func(ctx context.Context, req mcp.CallToolRequest, input ToolInput) (result *mcp.CallToolResult, output ToolOutput, err error) {
		// Recover from any panic inside the command.
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic during tool execution: %v", r)
			}
		}()

		// Apply optional middleware.
		if sel.Middleware != nil {
			executeFunc := func(ctx context.Context, req mcp.CallToolRequest, input ToolInput) (*mcp.CallToolResult, ToolOutput, error) {
				return s.runInProcess(ctx, req, input)
			}
			return sel.Middleware(ctx, req, input, executeFunc)
		}

		return s.runInProcess(ctx, req, input)
	}
}

// runInProcess executes a freshly created cobra command tree in-process.
//
// A new *cobra.Command tree is produced by calling s.cmdFactory() on every
// invocation. Because the tree is brand new, there is no shared mutable state
// to reset, no risk of flag values, ctx fields, RunE pointers, or Options
// struct fields leaking between calls.
//
// In-process execution is serialised by inProcessMu because the fd-level stdio
// redirection below rewrites the process-global descriptors 1 and 2; concurrent
// in-process calls would otherwise corrupt each other's captured output.
func (s *MCPServer) runInProcess(ctx context.Context, req mcp.CallToolRequest, input ToolInput) (*mcp.CallToolResult, ToolOutput, error) {
	inProcessMu.Lock()
	defer inProcessMu.Unlock()

	name := req.Params.Name
	slog.Info("in-process MCP tool request", "tool", name)

	// Retrieve the pre-computed cobra sub-command path for this tool.
	cmdPath := s.toolPaths[name]

	// Build CLI args from the stored command path and the flat tool input.
	args := buildInProcessArgsFromPath(cmdPath, input)
	slog.Info("in-process command args", "tool", name, "args", args)

	// Redirect os.Stdout/Stderr at the file-descriptor level so that even
	// code that holds a captured *os.File reference (e.g. Options.Out that
	// received os.Stdout at startup) writes into our capture pipes instead
	// of corrupting the MCP transport stream.
	var stdout, stderr bytes.Buffer
	rOut, wOut, err := os.Pipe()
	if err != nil {
		return nil, ToolOutput{}, fmt.Errorf("stdout pipe: %w", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		_ = rOut.Close()
		_ = wOut.Close()
		return nil, ToolOutput{}, fmt.Errorf("stderr pipe: %w", err)
	}
	// Replace stdout/stderr FDs with the pipe write ends so code holding a
	// captured *os.File reference also writes into the pipes. On Windows this
	// is a no-op (SetOut/SetErr capture the output instead).
	restore, err := redirectStdio(wOut, wErr)
	if err != nil {
		_ = rOut.Close()
		_ = wOut.Close()
		_ = rErr.Close()
		_ = wErr.Close()
		return nil, ToolOutput{}, fmt.Errorf("redirect stdio: %w", err)
	}

	// Ensure FDs are restored and all pipe ends closed on every exit path
	// (including panic). restore() is idempotent (sync.Once), and a second
	// Close on an *os.File is a harmless no-op returning os.ErrInvalid.
	defer func() {
		restore()
		_ = wOut.Close()
		_ = wErr.Close()
		_ = rOut.Close()
		_ = rErr.Close()
	}()

	// Drain the pipes concurrently while the command executes. Without this, a
	// command that writes more than the pipe buffer (~64KB) would block forever
	// on write, because the pipe write ends stay open (via dup2) until restore()
	// runs after ExecuteContext returns — a deadlock for verbose commands.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&stdout, rOut) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&stderr, rErr) }()

	// Produce a fresh command tree for this invocation and route cobra's own
	// output (cmd.OutOrStdout/ErrOrStderr) into the same pipes. This keeps a
	// single writer (the io.Copy goroutine) draining each buffer, avoiding
	// concurrent writes to bytes.Buffer between cobra and the drain loop.
	root := s.cmdFactory()
	root.SetOut(wOut)
	root.SetErr(wErr)
	root.SetArgs(args)

	// Detach the tool's execution context from the caller's cancellation
	// signal. The caller (e.g. an AI runner streaming loop) may cancel its
	// context as soon as it receives the tool result, which would abort any
	// in-flight I/O inside the command (e.g. a Lark API call) even though
	// the work itself has already succeeded. context.WithoutCancel preserves
	// all values (trace IDs, DynamicRuntime, span context, etc.) while
	// preventing the cancellation from propagating into the command's execution.
	execErr := root.ExecuteContext(context.WithoutCancel(ctx))

	// Close the write ends and restore FDs so the pipe write ends are fully
	// closed; the drain goroutines then read EOF and finish.
	_ = wOut.Close()
	_ = wErr.Close()
	restore()

	wg.Wait()

	exitCode := 0
	if execErr != nil {
		// cobra surfaces RunE errors here; treat them as exit code 1.
		slog.Warn("in-process command returned error", "tool", name, "error", execErr)
		exitCode = 1
		// Write the error message to stderr so the MCP client can see it.
		if stderr.Len() == 0 {
			_, _ = stderr.WriteString(execErr.Error())
		}
	}

	return nil, ToolOutput{
		StdOut:   stdout.String(),
		StdErr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// makeSubprocessHandler returns a tool handler that re-invokes the current
// binary as a separate OS process for each tool call. The subprocess
// survives MCP server termination, so long-running commands (e.g. onexcode
// create mcpserver) are not killed when the MCP client times out and closes
// the transport.
func (s *MCPServer) makeSubprocessHandler(sel Selector, _ *cobra.Command) func(context.Context, mcp.CallToolRequest, ToolInput) (*mcp.CallToolResult, ToolOutput, error) {
	return func(ctx context.Context, req mcp.CallToolRequest, input ToolInput) (result *mcp.CallToolResult, output ToolOutput, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic during subprocess tool execution: %v", r)
			}
		}()

		name := req.Params.Name
		slog.Info("subprocess MCP tool request", "tool", name)

		// Retrieve the pre-computed cobra sub-command path for this tool.
		cmdPath := s.toolPaths[name]

		// Build CLI args from the stored command path and the flat tool input.
		args := buildInProcessArgsFromPath(cmdPath, input)
		slog.Info("subprocess command args", "tool", name, "args", args)

		// Run as a subprocess — survives parent (MCP server) termination.
		// context.WithoutCancel keeps the subprocess alive even when the caller
		// cancels its request context after the transport is torn down.
		output, err = execSubprocess(context.WithoutCancel(ctx), args)
		if err != nil {
			return nil, ToolOutput{}, fmt.Errorf("subprocess failed: %w", err)
		}
		return nil, output, nil
	}
}

// cmdSubPath returns the cobra sub-command path segments for cmd, i.e. the
// full CommandPath() with the root command name stripped. For example, if the
// full path is "myapp sre open", the result is ["sre", "open"]. If cmd is the
// root command itself, the result is nil.
//
// The segments are computed at tool-registration time and stored in
// MCPServer.toolPaths so that runInProcess never needs to reverse-engineer
// the path from the tool name.
func cmdSubPath(cmd *cobra.Command) []string {
	path := cmd.CommandPath() // e.g. "myapp sre open"
	spaceIdx := strings.IndexByte(path, ' ')
	if spaceIdx == -1 {
		// Root command itself — no sub-path.
		return nil
	}
	rest := path[spaceIdx+1:] // "sre open"
	return strings.Split(rest, " ")
}

// buildInProcessArgsFromPath constructs the cobra argument slice from the
// pre-computed command sub-path and the flat tool input.
//
// cmdPath contains the cobra sub-command segments (root already stripped), e.g.
// ["sre", "open"]. Flags and positional arguments are appended after them by
// splitting the flat ToolInput using the FlagNames and ArgNames metadata.
func buildInProcessArgsFromPath(cmdPath []string, input ToolInput) []string {
	// Start with a copy of the command path segments.
	parts := make([]string, len(cmdPath))
	copy(parts, cmdPath)

	// Split the flat input into flag args and positional args, then append.
	flagMap, posArgs := splitFlatInput(input)
	parts = append(parts, buildFlagArgs(flagMap)...)
	parts = append(parts, posArgs...)

	return parts
}
