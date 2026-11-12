package mqserver

import (
	"context"
	"log/slog"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/onexstack/onexstack/pkg/server"

	"github.com/onexstack/onexai/internal/pkg/kafka"
)

// mqServer implements the server.Server interface using the kafka framework.
type mqServer struct {
	engine *kafka.Engine
	obs    *genericoptions.ObservabilityOptions
}

// Ensure *mqServer implements the server.Server interface.
var _ server.Server = (*mqServer)(nil)

// NewMQServer initializes and returns a new HTTP server based on kafka.
// It returns the server.Server interface to abstract implementation details.
func (c *ServerConfig) NewMQServer() (server.Server, error) {
	kafkaClient, err := c.KafkaOptions.NewClient()
	if err != nil {
		slog.Error("failed to create kafka client", "error", err)
		return nil, err
	}
	engine, err := kafka.NewEngineFromClient(kafkaClient)
	if err != nil {
		return nil, err
	}

	c.Handler.ApplyTo(engine)

	return &mqServer{engine: engine, obs: c.ObservabilityOptions}, nil
}

// Run starts the Kafka server and blocks until it stops.
func (s *mqServer) Run(ctx context.Context) error {
	go s.obs.Serve()
	s.engine.Start(ctx)
	return nil
}

// GracefulStop gracefully shuts down the server.
func (s *mqServer) GracefulStop(ctx context.Context) error {
	s.engine.Stop()
	return nil
}
