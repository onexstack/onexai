//go:build wireinject
// +build wireinject

package agent

import (
	"context"

	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/agent/biz"
	"github.com/onexstack/onexai/internal/agent/di"
	"github.com/onexstack/onexai/internal/agent/pkg/validation"
)

// NewServer initializes and creates the web server with all necessary dependencies using Wire.
func NewServer(ctx context.Context, cfg *Config) (*Server, error) {
	wire.Build(
		// Every provider this service has, grouped by domain in package di.
		// The groups exist so a new dependency is added next to the others of
		// its kind rather than in a list over here.
		di.ProviderSet,
		biz.ProviderSet,
		validation.ProviderSet,

		// The server itself: both structs are assembled field by field from the
		// providers above, so neither needs a hand-written constructor.
		wire.Struct(new(ServerConfig), "*"),
		NewWebServer,
		wire.Struct(new(Server), "*"),
	)
	return nil, nil
}
