// Package model holds the shared MCP configuration types used by the editor
// managers (Claude Desktop, VSCode, Cursor). It is a leaf package with no
// dependencies on the manager or cmd layers, so those layers can reference it
// without creating an import cycle.
package model

import "fmt"

// Server represents an MCP server configuration entry. It is a superset of the
// fields understood by Claude Desktop, VSCode, and Cursor; unused fields are
// omitted from JSON via omitempty.
type Server struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Print displays the server configuration details.
func (s Server) Print() {
	if s.Type != "" {
		fmt.Printf("  Type: %s\n", s.Type)
	}
	if s.Command != "" {
		fmt.Printf("  Command: %s\n", s.Command)
	}
	if s.URL != "" {
		fmt.Printf("  URL: %s\n", s.URL)
	}
	if len(s.Args) > 0 {
		fmt.Printf("  Args: %v\n", s.Args)
	}
	if len(s.Env) > 0 {
		fmt.Printf("  Environment:\n")
		for key, value := range s.Env {
			fmt.Printf("    %s: %s\n", key, value)
		}
	}
	if len(s.Headers) > 0 {
		fmt.Printf("  Headers:\n")
		for key, value := range s.Headers {
			fmt.Printf("    %s: %s\n", key, value)
		}
	}
}

// Input represents an editor input variable configuration (used by VSCode and
// Cursor).
type Input struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Description string `json:"description"`
	Password    bool   `json:"password,omitempty"`
}

// AddServer adds or updates a server in a server map, initialising the map if
// nil.
func AddServer[V any](m *map[string]V, name string, server V) {
	if *m == nil {
		*m = make(map[string]V)
	}
	(*m)[name] = server
}

// HasServer reports whether a server with the given name exists.
func HasServer[V any](m map[string]V, name string) bool {
	_, ok := m[name]
	return ok
}

// RemoveServer removes a server from the map.
func RemoveServer[V any](m map[string]V, name string) {
	delete(m, name)
}

// PrintServers prints all configured servers, or a placeholder when empty.
func PrintServers[V interface{ Print() }](m map[string]V) {
	if len(m) == 0 {
		fmt.Println("No MCP servers are currently configured.")
		return
	}

	for name, server := range m {
		fmt.Printf("Server: %s\n", name)
		server.Print()
		fmt.Println()
	}
}
