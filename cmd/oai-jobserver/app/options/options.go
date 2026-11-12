package options

import (
	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/onexstack/onexstack/pkg/watch"
	"github.com/spf13/pflag"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/onexstack/onexai/internal/jobserver"
	customoptions "github.com/onexstack/onexai/pkg/options"
)

// ServerOptions contains the configuration options for the server.
type ServerOptions struct {
	// WatchOptions contains the Watch configuration options.
	WatchOptions *watch.Options `json:"watch" mapstructure:"watch"`
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
		WatchOptions:  watch.NewOptions(),
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
	o.WatchOptions.AddFlags(fs, "watch")
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
	errs = append(errs, o.WatchOptions.Validate()...)
	errs = append(errs, o.CoreDBOptions.Validate()...)
	errs = append(errs, o.OTelOptions.Validate()...)
	errs = append(errs, o.FakeOptions.Validate()...)
	errs = append(errs, o.OssOptions.Validate()...)

	// Aggregate all errors and return them.
	return utilerrors.NewAggregate(errs)
}

// Config builds an jobserver.Config based on ServerOptions.
func (o *ServerOptions) Config() (*jobserver.Config, error) {
	return &jobserver.Config{
		WatchOptions:  o.WatchOptions,
		CoreDBOptions: o.CoreDBOptions,
		FakeOptions:   o.FakeOptions,
		OssOptions:    o.OssOptions,
	}, nil
}
