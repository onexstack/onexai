package cobrax

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

// Config customizes MCP server behavior and command-to-tool conversion.
type Config struct {
	// CommandName is the Use name for the top-level command returned by Command().
	// It is also used by GetCmdPath to locate the cobrax command in the Cobra tree
	// and by cmdFilter to exclude cobrax subcommands from tool exposure.
	// Default: "mcp".
	CommandName string

	// Selectors defines rules for converting commands to MCP tools.
	// Each selector specifies which commands to match and which flags to include.
	//
	// Basic safety filters are always applied first:
	//   - Hidden/deprecated commands and flags are excluded
	//   - Non-runnable commands are excluded
	//   - Built-in commands (the cobrax command, help, completion) are excluded
	//
	// Then selectors are evaluated in order for each command:
	//   1. The first selector whose CmdSelector returns true is used
	//   2. That selector's FlagSelector determines which flags are included
	//   3. If no selectors match, the command is not exposed as a tool
	//
	// If nil or empty, defaults to exposing all commands with all flags.
	Selectors []Selector

	// DefaultEnv specifies environment variables that are automatically
	// included when `enable` writes a server config for any editor.
	// These are merged with user-provided --env values; user values
	// take precedence on conflict.
	//
	// A common use is to capture PATH so the MCP server subprocess can
	// find executables that live outside the system PATH:
	//
	//   &cobrax.Config{
	//       DefaultEnv: map[string]string{
	//           "PATH": os.Getenv("PATH"),
	//       },
	//   }
	//
	// If nil, no default environment variables are added (current behavior).
	DefaultEnv map[string]string

	// ToolNamePrefix replaces the root command name in tool names.
	// This is useful for shortening tool names to comply with API limits (e.g., Claude's 64 char limit).
	// For example, if root command is "omnistrate-ctl" and ToolNamePrefix is "omctl",
	// a command "omnistrate-ctl cost by-cell list" becomes "omctl_cost_by-cell_list" instead of
	// "omnistrate-ctl_cost_by-cell_list".
	// If empty, the root command name is used as-is.
	ToolNamePrefix string

	// SloggerOptions configures logging to stderr.
	// Default: Info level logging.
	SloggerOptions *slog.HandlerOptions

	// BaseURL is the base URL for the SSE server (e.g. "http://localhost:8080").
	// Required when using serveHTTP (the stream command).
	// If empty, defaults to "http://localhost:8080".
	BaseURL string

	server         *mcpserver.MCPServer
	tools          []*mcp.Tool
	toolMetas      map[string]toolMeta // tool name → registration-time metadata
	toolNamePrefix string              // resolved prefix (either ToolNamePrefix or root command name)
}

// commandName returns the configured CommandName, defaulting to "mcp".
func (c *Config) commandName() string {
	if c != nil && c.CommandName != "" {
		return c.CommandName
	}
	return "mcp"
}

func (c *Config) serveStdio(cmd *cobra.Command) error {
	c.registerTools(cmd)
	return StdioTransport{}.Serve(cmd.Context(), c.server, ServeOptions{})
}

func (c *Config) serveHTTP(cmd *cobra.Command, addr string) error {
	c.registerTools(cmd)

	cmd.Printf("MCP SSE server listening on address %q\n", addr)
	return SSETransport{}.Serve(cmd.Context(), c.server, ServeOptions{
		Addr:    addr,
		BaseURL: c.BaseURL,
	})
}

// registerTools fully initializes a MCP server and populates c.tools
func (c *Config) registerTools(cmd *cobra.Command) {
	// Configure logging to stderr. NOTE: slog.SetDefault mutates the process-global
	// default logger. This is acceptable for a short-lived CLI command, but not
	// for embedding cobrax in a long-running process that manages its own slog.
	handler := slog.NewTextHandler(os.Stderr, c.SloggerOptions)
	slog.SetDefault(slog.New(handler))

	// get root cmd
	rootCmd := cmd
	for rootCmd.Parent() != nil {
		rootCmd = rootCmd.Parent()
	}

	// resolve tool name prefix
	if c.ToolNamePrefix != "" {
		c.toolNamePrefix = c.ToolNamePrefix
	} else {
		c.toolNamePrefix = rootCmd.Name()
	}

	// make server
	c.server = mcpserver.NewMCPServer(
		rootCmd.Name(),
		rootCmd.Version,
	)

	// initialise tool metadata map
	c.toolMetas = make(map[string]toolMeta)

	// ensure at least one selector exists for tool creation logic
	if len(c.Selectors) == 0 {
		c.Selectors = []Selector{{}}
	}

	// register tools
	c.registerToolsRecursive(rootCmd)
}

// registerToolsRecursive explores a cmd tree, making tools recursively out of the provided cmd and its children
func (c *Config) registerToolsRecursive(cmd *cobra.Command) {
	// register all subcommands
	for _, subCmd := range cmd.Commands() {
		c.registerToolsRecursive(subCmd)
	}

	// apply basic filters
	if c.cmdFilter(cmd) {
		return
	}

	// cycle through selectors until one matches the cmd
	for i, s := range c.Selectors {
		if s.CmdSelector != nil && !s.CmdSelector(cmd) {
			continue
		}

		// create tool from cmd — returns flat schema and per-tool metadata
		tool, meta := s.createToolFromCmd(cmd, c.toolNamePrefix)
		slog.Debug("created tool", "tool_name", tool.Name, "selector_index", i)

		// capture sel and meta for closure
		sel := s
		toolMeta := meta
		// register tool with server using mark3labs/mcp-go API
		c.server.AddTool(*tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// Decode the flat ToolInput from the arguments map.
			input := decodeToolInput(req, toolMeta)
			_, output, err := sel.execute(ctx, req, input)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return toolOutputToResult(output), nil
		})

		// add tool to manager's tool list (for `tools` command)
		c.tools = append(c.tools, tool)

		// store metadata for REST and other consumers
		c.toolMetas[tool.Name] = meta

		// only the first matching selector is used
		break
	}
}

// cmdFilter returns true if cmd should be filtered out.
// It uses the configured CommandName (defaulting to "mcp") to exclude
// the cobrax command group from being exposed as MCP tools, in addition to the
// shared safety filters (AnnotationMCPTool, hidden, deprecated, non-runnable).
func (c *Config) cmdFilter(cmd *cobra.Command) bool {
	return basicCmdFilter(cmd, c.commandName(), "help", "completion")
}

// decodeToolInput extracts a ToolInput from a mark3labs CallToolRequest.
// The MCP client sends all parameters as a flat top-level map.  meta carries
// the flag name set and arg spec slice that were computed at registration time
// and are required to reconstruct the cobra command arguments at execution time.
func decodeToolInput(req mcp.CallToolRequest, meta toolMeta) ToolInput {
	rawArgs := req.GetArguments()
	flat := make(map[string]any, len(rawArgs))
	for k, v := range rawArgs {
		flat[k] = v
	}

	argNames := make([]string, len(meta.argSpecs))
	for i, spec := range meta.argSpecs {
		argNames[i] = spec.Name
	}

	return ToolInput{
		FlatInput: flat,
		FlagNames: meta.flagNames,
		ArgNames:  argNames,
	}
}

// toolOutputToResult converts a ToolOutput into a *mcp.CallToolResult.
func toolOutputToResult(output ToolOutput) *mcp.CallToolResult {
	combined := output.StdOut
	if output.StdErr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += output.StdErr
	}
	if output.ExitCode != 0 {
		if combined == "" {
			combined = fmt.Sprintf("exit code %d", output.ExitCode)
		}
		return mcp.NewToolResultError(combined)
	}
	return mcp.NewToolResultText(combined)
}
