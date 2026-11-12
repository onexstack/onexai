// Package editor generates the "claude", "vscode", and "cursor" MCP management
// command groups from a shared specification, eliminating the copy-paste across
// the three editor integrations.
package editor

import (
	"fmt"
	"os"

	"github.com/feizaonet/cobrax/internal/cfgmgr/manager"
	"github.com/feizaonet/cobrax/internal/cfgmgr/model"
	"github.com/spf13/cobra"
)

// Spec describes a single editor integration. The three built-in editors are
// exposed via Claude, VSCode, and Cursor.
type Spec struct {
	// Use is the command name ("claude", "vscode", "cursor").
	Use string
	// ConfigWord is used in short labels like "Add server to X config" and
	// "Path to X config file".
	ConfigWord string
	// DisplayName is used in labels like "Manage X MCP servers" and the list
	// header.
	DisplayName string
	// LongName is used in the root command's long description.
	LongName string
	// WorkspaceDir is the workspace subdirectory (e.g. ".vscode", ".cursor").
	// When non-empty, the editor supports a --workspace flag.
	WorkspaceDir string
	// ServerType is the server "type" field written on enable (e.g. "stdio"),
	// or empty for editors that do not use one (Claude).
	ServerType string
	// NewManager constructs the manager for this editor.
	NewManager func(configPath string, workspace bool) (manager.ManagerLike, error)
}

// hasWorkspace reports whether the editor supports workspace-scoped config.
func (s Spec) hasWorkspace() bool { return s.WorkspaceDir != "" }

// Claude returns the "claude" command group.
func Claude(commandName string, defaultEnv map[string]string) *cobra.Command {
	return Command(Spec{
		Use:         "claude",
		ConfigWord:  "Claude",
		DisplayName: "Claude Desktop",
		LongName:    "Claude Desktop",
		NewManager: func(configPath string, _ bool) (manager.ManagerLike, error) {
			return manager.NewClaudeManager(configPath)
		},
	}, commandName, defaultEnv)
}

// VSCode returns the "vscode" command group.
func VSCode(commandName string, defaultEnv map[string]string) *cobra.Command {
	return Command(Spec{
		Use:          "vscode",
		ConfigWord:   "VSCode",
		DisplayName:  "VSCode",
		LongName:     "Visual Studio Code",
		WorkspaceDir: ".vscode",
		ServerType:   "stdio",
		NewManager: func(configPath string, workspace bool) (manager.ManagerLike, error) {
			return manager.NewVSCodeManager(configPath, workspace)
		},
	}, commandName, defaultEnv)
}

// Cursor returns the "cursor" command group.
func Cursor(commandName string, defaultEnv map[string]string) *cobra.Command {
	return Command(Spec{
		Use:          "cursor",
		ConfigWord:   "Cursor",
		DisplayName:  "Cursor",
		LongName:     "Cursor",
		WorkspaceDir: ".cursor",
		ServerType:   "stdio",
		NewManager: func(configPath string, workspace bool) (manager.ManagerLike, error) {
			return manager.NewCursorManager(configPath, workspace)
		},
	}, commandName, defaultEnv)
}

// Command builds the editor command group (enable/disable/list) from a Spec.
func Command(spec Spec, commandName string, defaultEnv map[string]string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   spec.Use,
		Short: "Manage " + spec.DisplayName + " MCP servers",
		Long:  "Manage MCP server configuration for " + spec.LongName,
	}

	cmd.AddCommand(
		enableCommand(spec, commandName, defaultEnv),
		disableCommand(spec),
		listCommand(spec),
	)
	return cmd
}

// enableCommand creates the "enable" subcommand.
func enableCommand(spec Spec, commandName string, defaultEnv map[string]string) *cobra.Command {
	f := &enableFlags{spec: spec, commandName: commandName, defaultEnv: defaultEnv}
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Add server to " + spec.ConfigWord + " config",
		Long:  "Add this application as an MCP server in " + spec.DisplayName,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return f.run(cmd)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&f.logLevel, "log-level", "", "Log level (debug, info, warn, error)")
	flags.StringVar(&f.configPath, "config-path", "", "Path to "+spec.ConfigWord+" config file")
	flags.StringVar(&f.serverName, "server-name", "", "Name for the MCP server (default: derived from executable name)")
	if spec.hasWorkspace() {
		flags.BoolVar(&f.workspace, "workspace", false,
			"Add to workspace settings ("+spec.WorkspaceDir+"/mcp.json) instead of user settings")
	}
	flags.StringToStringVarP(&f.env, "env", "e", nil, "Environment variables (e.g., --env KEY1=value1 --env KEY2=value2)")

	return cmd
}

// disableCommand creates the "disable" subcommand.
func disableCommand(spec Spec) *cobra.Command {
	f := &disableFlags{spec: spec}
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Remove server from " + spec.ConfigWord + " config",
		Long:  "Remove this application from " + spec.DisplayName + " MCP servers",
		RunE: func(_ *cobra.Command, _ []string) error {
			return f.run()
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&f.configPath, "config-path", "", "Path to "+spec.ConfigWord+" config file")
	flags.StringVar(&f.serverName, "server-name", "", "Name of the MCP server to remove (default: derived from executable name)")
	if spec.hasWorkspace() {
		flags.BoolVar(&f.workspace, "workspace", false,
			"Remove from workspace settings ("+spec.WorkspaceDir+"/mcp.json) instead of user settings")
	}

	return cmd
}

// listCommand creates the "list" subcommand.
func listCommand(spec Spec) *cobra.Command {
	f := &listFlags{spec: spec}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show " + spec.ConfigWord + " MCP servers",
		Long:  "Show all MCP servers configured in " + spec.DisplayName,
		RunE: func(_ *cobra.Command, _ []string) error {
			return f.run()
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&f.configPath, "config-path", "", "Path to "+spec.ConfigWord+" config file")
	if spec.hasWorkspace() {
		flags.BoolVar(&f.workspace, "workspace", false,
			"List from workspace settings ("+spec.WorkspaceDir+"/mcp.json) instead of user settings")
	}

	return cmd
}

type enableFlags struct {
	spec        Spec
	commandName string
	defaultEnv  map[string]string
	configPath  string
	logLevel    string
	serverName  string
	workspace   bool
	env         map[string]string
}

func (f *enableFlags) run(cmd *cobra.Command) error {
	executablePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to determine executable path: %w", err)
	}

	mcpPath, err := manager.GetCmdPath(cmd, f.commandName)
	if err != nil {
		return fmt.Errorf("failed to determine MCP command path: %w", err)
	}

	server := model.Server{
		Type:    f.spec.ServerType,
		Command: executablePath,
		Args:    append(mcpPath, "start"),
	}

	if f.logLevel != "" {
		server.Args = append(server.Args, "--log-level", f.logLevel)
	}

	// Merge default env with user-provided env; user values take precedence.
	env := make(map[string]string)
	for k, v := range f.defaultEnv {
		env[k] = v
	}
	for k, v := range f.env {
		env[k] = v
	}
	if len(env) > 0 {
		server.Env = env
	}

	if f.serverName == "" {
		f.serverName = manager.DeriveServerName(executablePath)
	}

	m, err := f.spec.NewManager(f.configPath, f.workspace)
	if err != nil {
		return err
	}

	return m.EnableServer(f.serverName, server)
}

type disableFlags struct {
	spec       Spec
	configPath string
	serverName string
	workspace  bool
}

func (f *disableFlags) run() error {
	if f.serverName == "" {
		executablePath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to determine executable path: %w", err)
		}
		f.serverName = manager.DeriveServerName(executablePath)
	}

	m, err := f.spec.NewManager(f.configPath, f.workspace)
	if err != nil {
		return err
	}

	return m.DisableServer(f.serverName)
}

type listFlags struct {
	spec       Spec
	configPath string
	workspace  bool
}

func (f *listFlags) run() error {
	m, err := f.spec.NewManager(f.configPath, f.workspace)
	if err != nil {
		return err
	}

	fmt.Printf("%s MCP servers:\n\n", f.spec.DisplayName)
	m.Print()
	return nil
}
