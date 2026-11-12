package cobrax

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"
)

// restCommandFlags holds flags for the rest command.
type restCommandFlags struct {
	logLevel string
	host     string
	port     int
	baseURL  string
}

// restCommand creates the 'mcp rest' subcommand.
//
// It starts a plain HTTP server that exposes every cobra command as a REST
// endpoint:
//
//	POST /{toolName}
//
// The request body is a flat JSON object (flags and positional args at the same
// level).  The response body contains stdout, stderr and the exit code.
//
// Example:
//
//	# start the server
//	myapp mcp rest --port 9090 --base-url http://api.example.com
//
//	# call a tool
//	curl -s -X POST http://localhost:9090/myapp_oncall \
//	  -H 'Content-Type: application/json' \
//	  -d '{"module":"bke","title":"pod crash","level":2}'
func restCommand(config *Config) *cobra.Command {
	f := &restCommandFlags{}
	cmd := &cobra.Command{
		Use:   "rest",
		Short: "Serve cobra commands as REST API",
		Long: `Start an HTTP server that exposes every cobra command as a REST endpoint.

Each command is reachable at:

  POST /{toolName}

where toolName matches the MCP tool name (e.g. "myapp_sre_open").

Request body — flat JSON object, flags and positional args at the same level:

  { "namespace": "prod", "module": "bke", "level": 3 }

Response body:

  { "stdout": "...", "stderr": "...", "exitCode": 0 }`,
		Example: `  # Start on the default port
  myapp mcp rest

  # Custom host, port and externally-visible base URL
  myapp mcp rest --host 0.0.0.0 --port 9090 --base-url https://api.example.com

  # Call a tool
  curl -s -X POST http://localhost:8080/myapp_oncall \
    -H 'Content-Type: application/json' \
    -d '{"module":"bke","title":"pod crash","level":2}'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if config == nil {
				config = &Config{}
			}

			if f.logLevel != "" {
				level := parseLogLevel(f.logLevel)
				if config.SloggerOptions == nil {
					config.SloggerOptions = &slog.HandlerOptions{}
				}
				config.SloggerOptions.Level = level
			}

			addr := fmt.Sprintf("%s:%d", f.host, f.port)
			return config.serveREST(cmd, addr, f.baseURL)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&f.logLevel, "log-level", "", "Log level (debug, info, warn, error)")
	flags.StringVar(&f.host, "host", "", "Host address to listen on (default: all interfaces)")
	flags.IntVar(&f.port, "port", 8080, "Port number to listen on")
	flags.StringVar(&f.baseURL, "base-url", "", "Externally-visible base URL logged at startup (e.g. https://api.example.com)")
	return cmd
}
