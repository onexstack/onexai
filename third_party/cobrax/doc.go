// Package cobrax transforms Cobra CLI applications into MCP (Model Context Protocol) servers,
// enabling AI assistants to interact with command-line tools.
//
// cobrax automatically converts existing Cobra commands into MCP tools, handling
// protocol complexity, command execution, and tool registration.
//
// # Two Execution Models
//
// cobrax supports two execution models:
//
//  1. Subprocess model ([Command] / [Config]): The MCP server re-invokes the
//     same binary as a subprocess for each tool call. This works well when all
//     necessary state can be reconstructed from CLI flags alone.
//
//  2. In-process model ([NewMCPServer] / [MCPServer]): The MCP server calls the
//     cobra.Command's Run/RunE function directly in-process. Because the command
//     closures already hold references to runtime dependencies (API clients,
//     database handles, business-logic objects, etc.), those objects are
//     available without any additional wiring. This is the preferred model when
//     commands depend on non-serialisable state.
//
// # In-Process Usage (NewMCPServer)
//
// Provide a factory function that produces a fresh *cobra.Command tree on each
// call. The factory is invoked once at registration time (read-only, for schema
// generation) and once per tool invocation at runtime. Using a factory ensures
// that every tool call gets its own Options structs, flag values, and
// closure-captured variables — eliminating all shared-state hazards and making
// concurrent tool calls fully independent.
//
//	package main
//
//	import (
//	    "context"
//	    "log"
//	    "github.com/feizaonet/cobrax"
//	)
//
//	func main() {
//	    // Dependencies are injected at construction time and shared safely
//	    // because they are read-only after initialisation.
//	    biz := newBotSreBiz(apiClient, dbClient)
//
//	    // Factory produces a fresh command tree per invocation.
//	    // Each call allocates new Options structs and closure state.
//	    factory := func() *cobra.Command {
//	        return buildRootCommand(biz) // adds all subcommands with biz in closures
//	    }
//
//	    srv, err := cobrax.NewMCPServer(cobrax.MCPOptions{
//	        Enabled: true,
//	        Addr:    ":8090",
//	        Name:    "myapp",
//	        Version: "1.0.0",
//	    }, factory)
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//
//	    if err := srv.Start(context.Background()); err != nil {
//	        log.Fatal(err)
//	    }
//	}
//
// # Subprocess Usage (Command)
//
// Add MCP server management subcommands to an existing Cobra application:
//
//	package main
//
//	import (
//	    "os"
//	    "github.com/feizaonet/cobrax"
//	)
//
//	func main() {
//	    rootCmd := createMyRootCommand()
//
//	    // Adds: mcp start, mcp tools, mcp claude enable/disable/list, etc.
//	    rootCmd.AddCommand(cobrax.Command(nil))
//
//	    if err := rootCmd.Execute(); err != nil {
//	        os.Exit(1)
//	    }
//	}
//
// # Configuration (Subprocess model)
//
// The [Config] struct provides fine-grained control over which commands and
// flags are exposed as MCP tools through a selector system.
//
// Basic filters are always applied automatically:
//   - Hidden and deprecated commands/flags are excluded
//   - Commands without executable functions are excluded
//   - Built-in commands (mcp, help, completion) are excluded
//
// Example with selectors:
//
//	config := &cobrax.Config{
//	    Selectors: []cobrax.Selector{
//	        {
//	            CmdSelector:           cobrax.AllowCmdsContaining("get", "list"),
//	            LocalFlagSelector:     cobrax.AllowFlags("namespace", "output"),
//	            InheritedFlagSelector: cobrax.NoFlags,
//	        },
//	        {
//	            CmdSelector:           cobrax.AllowCmds("mycli delete"),
//	            LocalFlagSelector:     cobrax.ExcludeFlags("all", "force"),
//	            InheritedFlagSelector: cobrax.NoFlags,
//	        },
//	    },
//	    SloggerOptions: &slog.HandlerOptions{Level: slog.LevelDebug},
//	}
package cobrax
