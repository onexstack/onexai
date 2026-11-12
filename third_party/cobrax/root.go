package cobrax

import (
	"github.com/feizaonet/cobrax/internal/cfgmgr/cmd/editor"
	"github.com/spf13/cobra"
)

// Command creates MCP server management commands for a Cobra CLI.
// Pass nil for default configuration or provide a Config for customization.
func Command(config *Config) *cobra.Command {
	name := config.commandName()

	var defaultEnv map[string]string
	if config != nil {
		defaultEnv = config.DefaultEnv
	}

	cmd := &cobra.Command{
		Use:   name,
		Short: "MCP server management",
		Long:  `Manage MCP servers for AI assistants and code editors`,
	}

	// Add subcommands
	cmd.AddCommand(
		startCommand(config),
		toolCommand(config),
		streamCommand(config),
		restCommand(config),
		editor.Claude(name, defaultEnv),
		editor.VSCode(name, defaultEnv),
		editor.Cursor(name, defaultEnv),
	)
	return cmd
}
