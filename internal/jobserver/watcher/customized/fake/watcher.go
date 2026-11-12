package fake

import (
	"context"
	"log/slog"
	"time"

	"github.com/gammazero/workerpool"
	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/onexstack/onexstack/pkg/watch"
	"github.com/onexstack/onexstack/pkg/watch/registry"

	"github.com/onexstack/onexai/internal/jobserver/model"
	"github.com/onexstack/onexai/internal/jobserver/pkg/metrics"
	"github.com/onexstack/onexai/internal/jobserver/store"
	"github.com/onexstack/onexai/internal/jobserver/watcher"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	known "github.com/onexstack/onexai/pkg/known/job"
)

const (
	// DefaultWatchInterval is the default interval for checking jobs.
	DefaultWatchInterval = "@every 1s"
)

// Watcher implements a simplified fake monitoring system.
type Watcher struct {
	watch.Watcher

	store store.IStore
}

// Ensure Watcher implements all required interfaces
var _ registry.Watcher = (*Watcher)(nil)

// Run executes the watcher with concurrency control, and rate limiting.
func (w *Watcher) Run() {
	_, fakes, err := w.store.Fake().List(context.Background(), where.NewWhere())
	if err != nil {
		slog.Error("failed to list fakes", "error", err)
		return
	}

	wp := workerpool.New(w.PerConcurrency)
	for _, fake := range fakes {
		ctx := contextx.WithLogger(context.Background(), slog.With("fake_id", fake.FakeID))
		wp.Submit(func() { _ = w.processJob(ctx, fake) })
	}

	wp.StopWait()
}

// processJob processes a single fake with error handling and rate limiting.
func (w *Watcher) processJob(ctx context.Context, fakeM *model.FakeM) (err error) {
	w.Limiter.Take()

	metrics.M.RecordJobExecution(ctx, "fake", fakeM.FakeID)
	defer func() {
		if err != nil {
			metrics.M.RecordJobFailure(ctx, "fake", fakeM.FakeID, err.Error())
		}
	}()

	contextx.L(ctx).InfoContext(ctx, "Started to process fake job")

	// Simulate fake processing (replace with actual business logic)
	// Add your actual fake processing logic here
	time.Sleep(100 * time.Millisecond)

	contextx.L(ctx).InfoContext(ctx, "Ended to process fake job")

	return nil
}

// Spec returns the cron job specification for scheduling.
func (w *Watcher) Spec() string {
	return DefaultWatchInterval
}

// SetAggregateConfig initializes the watcher for later execution.
func (w *Watcher) SetAggregateConfig(config *watcher.AggregateConfig) {
	w.store = config.Store
}

func init() {
	registry.Register(known.FakeWatcher, &Watcher{})
}
