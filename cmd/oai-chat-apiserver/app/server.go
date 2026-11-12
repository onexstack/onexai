package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/onexstack/onexstack/pkg/app"

	"github.com/onexstack/onexai/cmd/oai-chat-apiserver/app/options"
	"github.com/onexstack/onexai/internal/chat"
	"github.com/onexstack/onexai/pkg/lock"
)

// commandDesc is the detailed description of the command shown in help output.
const commandDesc = `oai-chat-apiserver is a Lark-based bot backend service.

It receives Lark messages and processes them according to their content.`

// NewApp creates and returns a new *app.App instance that represents the
// oai-chat-apiserver application. It wires up command-line flags, configuration
// loading, OpenTelemetry, and the server run function via the OneXStack
// application framework.
func NewApp() *app.App {
	opts := options.NewServerOptions()

	// The framework can invoke the pre-shutdown hook on more than one exit
	// path (signal, run error); shutting the OpenTelemetry providers down
	// twice would flush into a closed exporter, so guard it.
	var shutdownOnce sync.Once

	return app.NewApp(
		"oai-chat-apiserver",
		"oai-chat-apiserver: Lark-based bot backend service",
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
			var err error
			shutdownOnce.Do(func() { err = opts.OTelOptions.Shutdown(ctx) })
			return err
		}),
	)
}

// run returns the RunFunc that builds and runs the oai-chat-apiserver. The ctx
// parameter is signal-aware and is canceled on SIGINT/SIGTERM/SIGQUIT, enabling
// graceful shutdown.
func run(opts *options.ServerOptions) app.RunFunc {
	return func(ctx context.Context) error {
		// Convert options to server Config.
		cfg, err := opts.Config()
		if err != nil {
			return fmt.Errorf("failed to load configuration: %w", err)
		}

		// A login provider that cannot verify its credential is an
		// authentication bypass. local is not one of them any more: it checks
		// a bcrypt hash in chat.user. The other two still mint an identity from
		// unverified input, so whoever opted in gets a startup record of it
		// rather than a silent grant.
		if login := cfg.ChatOptions.Login; login.AllowLark || login.AllowAPIKey {
			slog.Warn("INSECURE: chat login provider(s) enabled without credential verification; "+
				"/v1/auth/login will mint identity tokens from unverified input",
				"allow-lark", login.AllowLark,
				"allow-apikey", login.AllowAPIKey,
			)
		}

		// Held for the lifetime of the process: the conversations this instance
		// owns include live child processes, which a second instance would
		// trample rather than share.
		lockPath, err := chat.InstanceLockPath(cfg)
		if err != nil {
			return fmt.Errorf("failed to resolve the instance lock: %w", err)
		}
		instanceLock, err := lock.Acquire(lockPath, "oai-chat-apiserver")
		if err != nil {
			return fmt.Errorf("failed to acquire the instance lock: %w", err)
		}
		defer instanceLock.Release() //nolint:errcheck

		// Construct server via Wire-generated init.
		srv, err := cfg.New(ctx)
		if err != nil {
			return fmt.Errorf("failed to create server: %w", err)
		}

		return srv.Run(ctx)
	}
}
