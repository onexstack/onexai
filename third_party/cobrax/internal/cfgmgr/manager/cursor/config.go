package cursor

import "github.com/feizaonet/cobrax/internal/cfgmgr/model"

// Config represents the structure of Cursor's MCP configuration file.
type Config struct {
	Inputs  []Input           `json:"inputs,omitempty"`
	Servers map[string]Server `json:"mcpServers"`
}

// AddServer adds or updates a server in the configuration.
func (c *Config) AddServer(name string, server Server) {
	model.AddServer(&c.Servers, name, server)
}

// HasServer returns true if a server with the given name exists in the configuration.
func (c *Config) HasServer(name string) bool {
	return model.HasServer(c.Servers, name)
}

// RemoveServer removes a server from the configuration.
func (c *Config) RemoveServer(name string) {
	model.RemoveServer(c.Servers, name)
}

// Print displays all configured MCP servers.
func (c *Config) Print() {
	model.PrintServers(c.Servers)
}
