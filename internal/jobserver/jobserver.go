package jobserver

import (
	"context"

	"github.com/onexstack/onexstack/pkg/server"
	"github.com/onexstack/onexstack/pkg/watch"
	sloglogger "github.com/onexstack/onexstack/pkg/watch/logger/slog"

	"github.com/onexstack/onexai/internal/jobserver/pkg/clientset/typed/fakeminio"
	"github.com/onexstack/onexai/internal/jobserver/store"
	"github.com/onexstack/onexai/internal/jobserver/watcher"
	_ "github.com/onexstack/onexai/internal/jobserver/watcher/all"
)

// jobServer implements the server.Server interface using the kafka framework.
type jobServer struct {
	watch *watch.Watch
}

// Ensure *jobServer implements the server.Server interface.
var _ server.Server = (*jobServer)(nil)

// NewJobServer initializes and returns a new HTTP server based on kafka.
// It returns the server.Server interface to abstract implementation details.
func (c *ServerConfig) NewJobServer() (server.Server, error) {
	minio, _ := fakeminio.NewForConfig("fake-bucket")
	config := &watcher.AggregateConfig{
		Minio: minio,
		Store: store.NewStore(c.DB),
	}

	initialize := watcher.NewInitializer(config)
	opts := []watch.Option{
		watch.WithInitialize(initialize),
		watch.WithLogger(sloglogger.NewLogger()),
		watch.WithAutoMigrate(true),
		watch.WithHealthzAddr(c.WatchOptions.HealthzAddr),
		watch.WithMetricsAddr(c.WatchOptions.MetricsAddr),
	}

	w, err := watch.NewWatch(c.WatchOptions, c.DB, opts...)
	if err != nil {
		return nil, err
	}

	return &jobServer{watch: w}, nil
}

// Run starts the job watcher and blocks until it stops.
func (s *jobServer) Run(ctx context.Context) error {
	s.watch.Start(ctx.Done())

	// Start returns as soon as the lease is held and the cron scheduler is
	// running. Without this wait Run would return immediately and the process
	// would exit 0 right after reporting itself started. GracefulStop is what
	// releases the lease once this returns.
	<-ctx.Done()
	return nil
}

// GracefulStop gracefully shuts down the server.
func (s *jobServer) GracefulStop(ctx context.Context) error {
	s.watch.Stop()
	return nil
}
