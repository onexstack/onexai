package app

import (
	"context"
	"fmt"

	"github.com/onexstack/onexstack/pkg/app"

	"github.com/onexstack/onexai/cmd/oai-agent-apiserver/app/options"
	"github.com/onexstack/onexai/internal/agent"
)

// commandDesc is the detailed description of the command shown in help output.
const commandDesc = `oai-agent-apiserver is the backend service of the OneX Agent Platform.

The OneX Agent Platform is a full-featured platform that helps enterprises
complete their internal intelligent transformation.`

// NewApp creates and returns a new *app.App instance that represents the
// oai-agent-apiserver application. It wires up command-line flags, configuration
// loading, OpenTelemetry, and the server run function via the OneXStack
// application framework.
func NewApp() *app.App {
	opts := options.NewServerOptions()

	return app.NewApp(
		"oai-agent-apiserver",
		"oai-agent-apiserver: backend for onexai chat mode",
		app.WithOptions(opts),
		app.WithRun(run(opts)),
		app.WithDirInHome(".onexai"),
		app.WithDescription(commandDesc),
		app.WithPreRunHook(func(ctx context.Context) error {
			// Initialize and configure OpenTelemetry providers.
			return opts.OTelOptions.Apply()
		}),
		app.WithPreShutdownHook(func(ctx context.Context) error {
			// Ensure OpenTelemetry resources are cleaned up exactly once.
			return opts.OTelOptions.Shutdown(ctx)
		}),
	)
}

// run returns the RunFunc that builds and runs the oai-agent-apiserver. The ctx
// parameter is signal-aware and is canceled on SIGINT/SIGTERM/SIGQUIT, enabling
// graceful shutdown.
func run(opts *options.ServerOptions) app.RunFunc {
	return func(ctx context.Context) error {
		// Convert options to server Config.
		cfg, err := opts.Config()
		if err != nil {
			return fmt.Errorf("failed to load configuration: %w", err)
		}

		// Construct server via Wire-generated init.
		srv, err := agent.New(ctx, cfg)
		if err != nil {
			return fmt.Errorf("failed to create server: %w", err)
		}

		return srv.Run(ctx)
	}
}
