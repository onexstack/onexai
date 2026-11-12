package cobrax

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/spf13/cobra"
)

// serveREST exposes every cobra command as a plain REST endpoint.
//
// # Route convention
//
//	POST /{toolName}
//
// toolName is identical to the MCP tool name produced by [toolName], e.g.
// "myapp_sre_open" or "myapp_oncall".
//
// # Request
//
// Content-Type: application/json
// Body: a flat JSON object whose keys are the parameter names exactly as they
// appear in the tool's inputSchema — flags and positional arguments at the same
// level, no nesting:
//
//	{ "namespace": "prod", "module": "bke", "title": "pod crash", "level": 3 }
//
// An empty body (or omitted Content-Type) is treated as zero parameters.
//
// # Response
//
// HTTP 200 — command executed (check exitCode for success/failure):
//
//	{ "stdout": "...", "stderr": "...", "exitCode": 0 }
//
// HTTP 400 — malformed JSON body:
//
//	{ "error": "invalid JSON body: ..." }
//
// HTTP 404 — unknown tool name:
//
//	{ "error": "tool \"x\" not found" }
//
// HTTP 405 — wrong HTTP method (only POST is accepted):
//
//	{ "error": "method GET not allowed; use POST" }
//
// HTTP 500 — subprocess could not be launched:
//
//	{ "error": "..." }
func (c *Config) serveREST(cmd *cobra.Command, addr, baseURL string) error {
	// Initialise slogger, walk the command tree, populate c.tools / c.toolMetas.
	c.registerTools(cmd)

	mux := http.NewServeMux()

	// Register one POST handler per tool.
	for _, tool := range c.tools {
		name := tool.Name
		meta, ok := c.toolMetas[name]
		if !ok {
			meta = toolMeta{flagNames: make(map[string]struct{})}
		}

		mux.HandleFunc("/"+name, restToolHandler(name, meta))
	}

	// Catch-all: JSON 404 for any path not matched above.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path
		if len(name) > 0 && name[0] == '/' {
			name = name[1:]
		}
		restWriteError(w, http.StatusNotFound, fmt.Sprintf("tool %q not found", name))
	})

	effectiveBaseURL := baseURL
	if effectiveBaseURL == "" {
		effectiveBaseURL = "http://" + addr
	}
	cmd.Printf("REST API server listening on %q (base URL: %s)\n", addr, effectiveBaseURL)
	slog.Info("REST API server listening", "addr", addr, "baseURL", effectiveBaseURL)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// Graceful shutdown when the cobra command context is cancelled or an
	// interrupt signal is received.
	go func() {
		<-shutdownSignal(cmd.Context())
		if err := srv.Shutdown(context.Background()); err != nil {
			slog.Error("REST server shutdown error", "error", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("REST server error: %w", err)
	}
	return nil
}

// restToolHandler returns an http.HandlerFunc for a single tool.
// The handler decodes the flat JSON request body into a ToolInput, builds the
// cobra CLI argument list via the same splitFlatInput / buildFlagArgs pipeline
// used by the MCP subprocess model, runs the binary as a subprocess, and
// writes the ToolOutput as JSON.
func restToolHandler(toolName string, meta toolMeta) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			restWriteError(w, http.StatusMethodNotAllowed,
				fmt.Sprintf("method %s not allowed; use POST", r.Method))
			return
		}

		// Decode the flat JSON request body.
		var flat map[string]any
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&flat); err != nil {
			// EOF means an empty body — treat as no parameters.
			if errors.Is(err, io.EOF) {
				flat = map[string]any{}
			} else {
				restWriteError(w, http.StatusBadRequest,
					fmt.Sprintf("invalid JSON body: %v", err))
				return
			}
		}

		slog.Info("REST tool request", "tool", toolName, "params", flat)

		// Build the ordered arg name list from the meta's argSpecs.
		argNames := make([]string, len(meta.argSpecs))
		for i, spec := range meta.argSpecs {
			argNames[i] = spec.Name
		}

		input := ToolInput{
			FlatInput: flat,
			FlagNames: meta.flagNames,
			ArgNames:  argNames,
		}

		// Reconstruct the cobra CLI argument slice (same path as MCP subprocess).
		args := buildCommandArgs(toolName, input)
		slog.Debug("REST subprocess args", "tool", toolName, "args", args)

		output, err := execSubprocess(r.Context(), args)
		if err != nil {
			restWriteError(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(output)
	}
}

// restWriteError writes a JSON error response.
func restWriteError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
