//go:build wireinject
// +build wireinject

package jobserver

import (
	"context"

	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/jobserver/pkg/clientset"
)

// infrastructureSet groups all infrastructure-related providers.
// This keeps the main wire.Build call clean.
var infrastructureSet = wire.NewSet(
	ProvideDB,
	ProvideFakeClient,
	ProvideOssClient,
	clientset.New,
	wire.Bind(new(clientset.Interface), new(*clientset.Clientset)),
	ProvideAStore,
)

// NewServer initializes and creates the web server with all necessary dependencies using Wire.
func NewServer(context.Context, *Config) (*Server, error) {
	wire.Build(
		// Server infrastructure
		NewJobServer,
		NewDependencies,
		wire.Struct(new(ServerConfig), "*"), // Inject all fields
		wire.Struct(new(Server), "*"),
		// Infrastructure dependencies
		infrastructureSet,
	)
	return nil, nil
}
