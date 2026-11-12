// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package options

import (
	"fmt"
	"time"

	"github.com/spf13/pflag"
)

// DefaultMCPRefreshInterval is how often the MCP bridge re-reads the
// custom-command catalog and rebuilds its tool set.
const DefaultMCPRefreshInterval = 10 * time.Second

// MCPOptions configures the Model Context Protocol listener oai-chat-apiserver
// exposes for oai-agent-apiserver's agents.
//
// The listener is a second, narrow surface: it serves the same command tree the
// chat surfaces serve, but as MCP tools with generated JSON Schema. It is off
// unless a deployment turns it on, because turning it on publishes this
// service's commands to whoever can reach the port.
type MCPOptions struct {
	// Enabled turns the MCP listener on. While false nothing is bound and no
	// command is registered as a tool.
	Enabled bool `json:"enabled" mapstructure:"enabled"`

	// Addr is the listen address, e.g. ":8091". Required when Enabled.
	Addr string `json:"addr" mapstructure:"addr"`

	// Name is the server name advertised to MCP clients. It is also what an
	// operator types into `hack/mcpctl` when registering this server, so it
	// defaults to the service name rather than being derived from the address.
	Name string `json:"name" mapstructure:"name"`

	// Version is the server version advertised to MCP clients. MCP requires a
	// non-empty version string in the initialize handshake.
	Version string `json:"version" mapstructure:"version"`

	// Token is the bearer token every MCP request must present. Empty means the
	// listener is open to anyone who can reach Addr — acceptable on a loopback
	// address, which is why it is not required.
	//
	// It is a secret: never log it, never echo it in an error, never commit it.
	Token string `json:"token" mapstructure:"token"`

	// UserID is the identity attributed to a tool call that arrives without the
	// identity header (internal/agent/pkg/mcpclient sends one per request;
	// see pkg/known.XUserID). It exists for two cases: a deployment whose
	// caller cannot be taught to send the header, and manual testing by an
	// operator.
	//
	// Empty means "no identity", which is the safe default: the ten tools that
	// attribute records to a caller (job/cronjob create/update/delete/get/list)
	// then refuse with "无法识别调用者身份" instead of writing rows owned by
	// nobody. Setting it makes every such call from every caller share one
	// owner, so it is a deliberate trade, not a convenience.
	UserID string `json:"user-id" mapstructure:"user-id"`

	// RefreshInterval is how often the bridge re-reads the custom-command
	// catalog. cobrax derives its tool schemas from the command tree it is first
	// handed, so without a re-read a command created after startup would not
	// become a tool until this process restarted. Zero or negative means the
	// default; there is no "disabled" value — configure a very large interval
	// for that.
	RefreshInterval time.Duration `json:"refresh-interval" mapstructure:"refresh-interval"`
}

// NewMCPOptions creates MCPOptions with default values: disabled, on :8091,
// named after this service, with no token and no fallback identity.
func NewMCPOptions() *MCPOptions {
	return &MCPOptions{
		Addr:            ":8091",
		Name:            "oai-chat-apiserver",
		Version:         "1.0.0",
		RefreshInterval: DefaultMCPRefreshInterval,
	}
}

// AddFlags binds MCPOptions to command-line flags under the given prefix.
func (o *MCPOptions) AddFlags(fs *pflag.FlagSet, fullPrefix string) {
	fs.BoolVar(&o.Enabled, fullPrefix+".enabled", o.Enabled, "Expose this service's commands as MCP tools on a dedicated listener.")
	fs.StringVar(&o.Addr, fullPrefix+".addr", o.Addr, "Listen address of the MCP listener (e.g. \":8091\").")
	fs.StringVar(&o.Name, fullPrefix+".name", o.Name, "MCP server name advertised to clients.")
	fs.StringVar(&o.Version, fullPrefix+".version", o.Version, "MCP server version advertised to clients.")
	fs.StringVar(&o.Token, fullPrefix+".token", o.Token, "Bearer token every MCP request must present (secret; empty means no auth).")
	fs.StringVar(&o.UserID, fullPrefix+".user-id", o.UserID, "Identity attributed to tool calls that carry no identity header (empty means none).")
	fs.DurationVar(&o.RefreshInterval, fullPrefix+".refresh-interval", o.RefreshInterval, "How often the bridge re-reads the custom-command catalog and rebuilds its tools (0 uses the default).")
}

// Validate checks MCPOptions. It refuses a half-configured listener rather than
// starting one that cannot be dialled or cannot identify itself.
func (o *MCPOptions) Validate() []error {
	if !o.Enabled {
		return nil
	}

	var errs []error
	if o.Addr == "" {
		errs = append(errs, fmt.Errorf("mcp.addr must not be empty when mcp.enabled is true"))
	}
	if o.Name == "" {
		errs = append(errs, fmt.Errorf("mcp.name must not be empty when mcp.enabled is true"))
	}
	if o.Version == "" {
		// The MCP initialize handshake rejects an empty version, and the failure
		// surfaces at the client as an opaque protocol error. Refusing to start
		// says which setting is wrong.
		errs = append(errs, fmt.Errorf("mcp.version must not be empty when mcp.enabled is true"))
	}
	if o.RefreshInterval < 0 {
		errs = append(errs, fmt.Errorf("mcp.refresh-interval must not be negative"))
	}
	return errs
}
