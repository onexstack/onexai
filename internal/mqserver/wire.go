//go:build wireinject
// +build wireinject

package mqserver

import (
	"context"

	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/mqserver/biz"
	"github.com/onexstack/onexai/internal/mqserver/handler"
	"github.com/onexstack/onexai/internal/mqserver/pkg/clientset"
	"github.com/onexstack/onexai/internal/mqserver/pkg/validation"
	"github.com/onexstack/onexai/internal/mqserver/store"
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
		NewMQServer,
		NewDependencies,
		wire.Struct(new(ServerConfig), "*"), // Inject all fields
		wire.Struct(new(Server), "*"),
		// Domain layers
		store.ProviderSet,
		biz.ProviderSet,
		validation.ProviderSet,
		handler.NewHandler,
		// Infrastructure dependencies
		infrastructureSet,
	)
	return nil, nil
}
