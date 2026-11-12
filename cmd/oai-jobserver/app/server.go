package app

import (
	"context"
	"fmt"

	"github.com/onexstack/onexstack/pkg/app"

	"github.com/onexstack/onexai/cmd/oai-jobserver/app/options"
)

// commandDesc is the detailed description of the command shown in help output.
const commandDesc = `TODO: Update the detailed description of the binary file.`

// NewApp creates and returns a new *app.App instance that represents the
// oai-jobserver application. It wires up command-line flags, configuration
// loading, OpenTelemetry, and the server run function via the OneXStack
// application framework.
func NewApp() *app.App {
	opts := options.NewServerOptions()

	return app.NewApp(
		"oai-jobserver",
		"TODO: Update the short description of the binary file.",
		app.WithOptions(opts),
		app.WithRun(run(opts)),
		app.WithDirInHome(".onexai"),
		app.WithDescription(commandDesc),
		app.WithPreRunHook(func(ctx context.Context) error {
			// Initialize and configure OpenTelemetry providers.
			return opts.OTelOptions.Apply()
		}),
		app.WithPreShutdownHook(func(ctx context.Context) error {
			// Ensure OpenTelemetry resources are cleaned up on shutdown.
			return opts.OTelOptions.Shutdown(ctx)
		}),
	)
}

// run returns the RunFunc that builds and runs the oai-jobserver. The ctx
// parameter is signal-aware and is canceled on SIGINT/SIGTERM/SIGQUIT, enabling
// graceful shutdown.
func run(opts *options.ServerOptions) app.RunFunc {
	return func(ctx context.Context) error {
		// Retrieve the application configuration from the parsed options.
		cfg, err := opts.Config()
		if err != nil {
			return fmt.Errorf("failed to load configuration: %w", err)
		}

		// Create a new server instance based on the configuration.
		server, err := cfg.New(ctx)
		if err != nil {
			return fmt.Errorf("failed to create server: %w", err)
		}

		// Run the server until the context is canceled or an error occurs.
		return server.Run(ctx)
	}
}
