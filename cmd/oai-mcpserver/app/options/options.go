package options

import (
	"fmt"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/spf13/pflag"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/onexstack/onexai/internal/mcpserver"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// validTransports is the set of MCP transports accepted by --mcp-transport.
var validTransports = map[string]bool{
	mcpserver.TransportStdio:          true,
	mcpserver.TransportSSE:            true,
	mcpserver.TransportStreamableHTTP: true,
}

// ServerOptions contains the configuration options for the server.
type ServerOptions struct {
	// Transport selects the MCP wire transport: stdio, sse, or streamable-http.
	Transport string `json:"mcp-transport" mapstructure:"mcp-transport"`
	// Addr is the listen address used by the sse and streamable-http
	// transports. It is ignored when Transport is stdio.
	Addr string `json:"addr" mapstructure:"addr"`
	// CoreDBOptions contains the database configuration (dialect, address, credentials).
	CoreDBOptions *customoptions.CoreDBOptions `json:"coredb" mapstructure:"coredb"`
	// OTelOptions used to specify the otel options.
	OTelOptions *genericoptions.OTelOptions `json:"otel" mapstructure:"otel"`
	// FakeOptions specifies whether to create a fake client.
	FakeOptions *genericoptions.RestyOptions `json:"fake" mapstructure:"fake"`
	// OssOptions specifies whether to create a oss client.
	OssOptions *genericoptions.RestyOptions `json:"oss" mapstructure:"oss"`
}

// NewServerOptions creates a ServerOptions instance with default values.
func NewServerOptions() *ServerOptions {
	opts := &ServerOptions{
		Transport:     mcpserver.TransportStdio,
		Addr:          ":8081",
		CoreDBOptions: customoptions.NewCoreDBOptions(),
		OTelOptions:   genericoptions.NewOTelOptions(),
		FakeOptions:   genericoptions.NewRestyOptions(),
		OssOptions:    genericoptions.NewRestyOptions(),
	}

	return opts
}

// AddFlags binds the options in ServerOptions to command-line flags.
func (o *ServerOptions) AddFlags(fs *pflag.FlagSet) {
	// Add command-line flags for sub-options.
	fs.StringVarP(&o.Transport, "mcp-transport", "t", o.Transport, "MCP transport to use: stdio, sse, or streamable-http.")
	fs.StringVar(&o.Addr, "addr", o.Addr, "Listen address for the sse and streamable-http transports (ignored for stdio).")
	o.CoreDBOptions.AddFlags(fs, "coredb")
	o.OTelOptions.AddFlags(fs, "otel")
	o.FakeOptions.AddFlags(fs, "fake")
	o.OssOptions.AddFlags(fs, "oss")
}

// Complete completes all the required options.
func (o *ServerOptions) Complete() error {
	// TODO: Add the completion logic if needed.
	return nil
}

// Validate checks whether the options in ServerOptions are valid.
func (o *ServerOptions) Validate() error {
	errs := []error{}

	// Validate sub-options.
	if !validTransports[o.Transport] {
		errs = append(errs, fmt.Errorf("invalid --mcp-transport %q: must be one of stdio, sse, streamable-http", o.Transport))
	}
	errs = append(errs, o.CoreDBOptions.Validate()...)
	errs = append(errs, o.OTelOptions.Validate()...)
	errs = append(errs, o.FakeOptions.Validate()...)
	errs = append(errs, o.OssOptions.Validate()...)

	// Aggregate all errors and return them.
	return utilerrors.NewAggregate(errs)
}

// Config builds an mcpserver.Config based on ServerOptions.
func (o *ServerOptions) Config() (*mcpserver.Config, error) {
	return &mcpserver.Config{
		Transport:     o.Transport,
		Addr:          o.Addr,
		CoreDBOptions: o.CoreDBOptions,
		FakeOptions:   o.FakeOptions,
		OssOptions:    o.OssOptions,
	}, nil
}
