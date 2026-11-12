package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/feizaonet/cobrax/internal/cfgmgr/manager/claude"
	"github.com/feizaonet/cobrax/internal/cfgmgr/manager/cursor"
	"github.com/feizaonet/cobrax/internal/cfgmgr/manager/vscode"
	"github.com/feizaonet/cobrax/internal/cfgmgr/model"
)

// Config represents MCP server configuration that can be managed.
type Config[S Server] interface {
	HasServer(name string) bool
	AddServer(name string, server S)
	RemoveServer(name string)
	Print()
}

// Server represents an individual MCP server entry.
type Server interface {
	Print()
}

// ManagerLike is a non-generic view of a *Manager[S, C]. It is used by the
// command layer so enable/disable/list commands can be generated once and
// operate on any editor's manager without referencing its concrete generic
// instantiation. The editor server types are all aliases of model.Server, so
// every *Manager[model.Server, *Config] satisfies this interface.
type ManagerLike interface {
	EnableServer(name string, server model.Server) error
	DisableServer(name string) error
	Print()
}

// Manager provides generic configuration management for MCP servers.
// It handles loading, saving, and modifying MCP server configurations.
// It is not thread-safe.
type Manager[S Server, C Config[S]] struct {
	configPath string
	config     C
}

// NewVSCodeManager creates a new Manager configured for VSCode MCP servers.
// If workspace is true, uses workspace configuration (.vscode/mcp.json),
// otherwise uses user-level configuration.
func NewVSCodeManager(configPath string, workspace bool) (*Manager[vscode.Server, *vscode.Config], error) {
	if configPath == "" {
		configPath = vscode.ConfigPath(workspace)
	}

	m := &Manager[vscode.Server, *vscode.Config]{
		config:     &vscode.Config{},
		configPath: configPath,
	}

	return m, m.loadConfig()
}

// NewCursorManager creates a new Manager configured for Cursor MCP servers.
// If workspace is true, uses workspace configuration (.cursor/mcp.json),
// otherwise uses user-level configuration.
func NewCursorManager(configPath string, workspace bool) (*Manager[cursor.Server, *cursor.Config], error) {
	if configPath == "" {
		configPath = cursor.ConfigPath(workspace)
	}

	m := &Manager[cursor.Server, *cursor.Config]{
		config:     &cursor.Config{},
		configPath: configPath,
	}

	return m, m.loadConfig()
}

// NewClaudeManager creates a new Manager configured for Claude Desktop MCP servers.
func NewClaudeManager(configPath string) (*Manager[claude.Server, *claude.Config], error) {
	if configPath == "" {
		configPath = claude.ConfigPath()
	}

	m := &Manager[claude.Server, *claude.Config]{
		config:     &claude.Config{},
		configPath: configPath,
	}

	return m, m.loadConfig()
}

// EnableServer adds or updates an MCP server in the configuration.
func (m *Manager[S, C]) EnableServer(name string, server S) error {
	// Snapshot the current config so we can roll back the in-memory change if
	// the save fails (the on-disk file is left untouched by the atomic write).
	snapshot, err := json.Marshal(m.config)
	if err != nil {
		return fmt.Errorf("failed to snapshot configuration: %w", err)
	}

	if m.config.HasServer(name) {
		fmt.Printf("⚠️  MCP server %q already exists and will be overwritten\n", name)
	}

	m.config.AddServer(name, server)
	err = m.saveConfig()
	if err != nil {
		_ = json.Unmarshal(snapshot, m.config)
		return err
	}

	fmt.Printf("Successfully enabled MCP server: %q\n", name)
	server.Print()
	return nil
}

// DisableServer removes an MCP server from the configuration.
func (m *Manager[S, C]) DisableServer(name string) error {
	if !m.config.HasServer(name) {
		fmt.Printf("⚠️  MCP server %q does not exist\n", name)
		return nil
	}

	// Snapshot for rollback if the save fails.
	snapshot, err := json.Marshal(m.config)
	if err != nil {
		return fmt.Errorf("failed to snapshot configuration: %w", err)
	}

	m.config.RemoveServer(name)
	if err := m.saveConfig(); err != nil {
		_ = json.Unmarshal(snapshot, m.config)
		return err
	}

	fmt.Printf("Successfully disabled MCP server: %q\n", name)
	return nil
}

// loadConfig unmarshals a JSON file into the provided interface.
func (m *Manager[S, C]) loadConfig() error {
	fmt.Printf("Using config path %q\n", m.configPath)

	// Check if config file exists
	if _, err := os.Stat(m.configPath); os.IsNotExist(err) {
		// File doesn't exist - return nil to allow initialization
		return nil
	}

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return fmt.Errorf("failed to read configuration file at %q: %w", m.configPath, err)
	}

	if err := json.Unmarshal(data, m.config); err != nil {
		return fmt.Errorf("failed to parse configuration file at %q: invalid JSON format: %w", m.configPath, err)
	}

	return nil
}

// saveConfig marshals and saves configuration as formatted JSON.
// The write is atomic: data is written to a temporary file in the same
// directory, fsynced, then renamed over the target so a partial write or crash
// cannot corrupt the existing configuration.
func (m *Manager[S, C]) saveConfig() error {
	// Ensure the directory exists
	if err := os.MkdirAll(filepath.Dir(m.configPath), 0o755); err != nil {
		return fmt.Errorf("failed to create directory for configuration file at %q: %w", filepath.Dir(m.configPath), err)
	}

	data, err := json.MarshalIndent(m.config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal configuration to JSON: %w", err)
	}

	// Backup is best-effort: a backup failure must not block the save.
	if err := m.backupConfig(); err != nil {
		slog.Warn("failed to back up config, continuing with save", "error", err)
	}

	// Write to a temporary file in the same directory so the rename is atomic.
	tmp, err := os.CreateTemp(filepath.Dir(m.configPath), ".mcp-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary file for %q: %w", m.configPath, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup of the temp file if the rename did not happen.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write configuration to %q: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to sync configuration to %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file %q: %w", tmpName, err)
	}
	// 0600: config files may contain sensitive values such as API tokens.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("failed to set permissions on %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, m.configPath); err != nil {
		// On Windows, rename fails if the target already exists. Fall back to
		// remove-then-rename (non-atomic but functionally correct).
		if removeErr := os.Remove(m.configPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("failed to replace configuration file at %q: %w", m.configPath, err)
		}
		if err := os.Rename(tmpName, m.configPath); err != nil {
			return fmt.Errorf("failed to rename configuration file to %q: %w", m.configPath, err)
		}
	}

	return nil
}

// Print calls Print on the underlying config
func (m *Manager[S, C]) Print() {
	m.config.Print()
}

func (m *Manager[S, C]) backupConfig() error {
	// Check if config file exists
	if _, err := os.Stat(m.configPath); os.IsNotExist(err) {
		// File doesn't exist - nothing to backup
		return nil
	}

	sourceFile, err := os.Open(m.configPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = sourceFile.Close()
	}()

	ext := filepath.Ext(m.configPath)
	dest := strings.TrimSuffix(m.configPath, ext) + ".backup.json"
	fmt.Printf("Backing up config file at %q\n", dest)
	destFile, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() {
		_ = destFile.Close()
	}()

	_, err = io.Copy(destFile, sourceFile)
	return err
}
