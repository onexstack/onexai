// Package cronjob implements a history watcher for managing job history cleanup.
// It provides functionality to maintain job history records within configured limits
// by removing old records based on success and failure history limits.
package cronjob

import (
	"context"
	"log/slog"
	"slices"

	"github.com/gammazero/workerpool"
	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/onexstack/onexstack/pkg/watch"
	"github.com/onexstack/onexstack/pkg/watch/registry"

	"github.com/onexstack/onexai/internal/jobserver/model"
	"github.com/onexstack/onexai/internal/jobserver/pkg/metrics"
	"github.com/onexstack/onexai/internal/jobserver/store"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	known "github.com/onexstack/onexai/pkg/known/job"
)

// Ensure History implements the registry.Watcher interface.
var _ registry.Watcher = (*History)(nil)

const (
	// DefaultHistoryInterval is the default interval for history cleanup.
	DefaultHistoryInterval = "@every 1s"
)

// History implements a watcher that manages job history cleanup.
// It monitors cron jobs and removes old job records based on configured limits
// to prevent unlimited growth of job history data.
type History struct {
	watch.Watcher

	store store.IStore
}

// NewHistory creates a new History watcher instance.
func NewHistory(store store.IStore) *History {
	return &History{store: store}
}

// Run executes the history cleanup process.
// It processes all non-suspended cron jobs and cleans up their associated job history
// based on the success and failure history limits configured for each cron job.
func (w *History) Run() {
	ctx, cancel := context.WithTimeout(context.Background(), w.WatchTimeout)
	defer cancel()

	_, cronjobs, err := w.store.CronJob().List(ctx, where.F("suspend", known.JobNonSuspended))
	if err != nil {
		return
	}

	wp := workerpool.New(w.PerConcurrency)
	for _, cronJob := range cronjobs {
		ctx := contextx.WithLogger(context.Background(), slog.With(
			"scope", cronJob.Scope,
			"cronjob_id", cronJob.CronJobID,
			"cron_job_name", cronJob.Name,
		))
		wp.Submit(func() { _ = w.processJob(ctx, cronJob) })
	}

	wp.StopWait()
}

// processJob processes a single job with error handling and rate limiting.
func (w *History) processJob(ctx context.Context, cronJob *model.CronJobM) (err error) {
	w.Limiter.Take()

	metrics.M.RecordCronJobExecution(ctx, cronJob.CronJobID)
	defer func() {
		if err != nil {
			metrics.M.RecordCronJobFailure(ctx, cronJob.CronJobID, err.Error())
		}
	}()

	w.retainRecords(ctx, cronJob.CronJobID, known.JobSucceeded, cronJob.SuccessHistoryLimit)
	w.retainRecords(ctx, cronJob.CronJobID, known.JobFailed, cronJob.FailedHistoryLimit)

	return nil
}

// retainRecords retains only the specified number of most recent records for a given status.
// It returns the number of records that were deleted.
func (w *History) retainRecords(ctx context.Context, cronJobID, status string, maxRecords int32) (int, error) {
	if maxRecords <= 0 {
		return 0, nil
	}

	// Build query conditions for the specific cron job and status.
	whr := where.F("cronjob_id", cronJobID, "status", status)

	// Fetch jobs ordered by creation time (newest first).
	_, jobs, err := w.store.Job().List(ctx, whr)
	if err != nil {
		return 0, err
	}

	// Calculate which records to remove.
	recordsToRemove := w.calculateRecordsToRemove(jobs, maxRecords)
	if len(recordsToRemove) == 0 {
		return 0, nil
	}

	// Delete the excess records.
	if err := w.store.Job().Delete(ctx, where.F("job_id", recordsToRemove)); err != nil {
		contextx.L(ctx).ErrorContext(ctx, "failed to delete jobs", "error", err, "status", status)
	}

	contextx.L(ctx).DebugContext(ctx, "Retained job records",
		"status", status,
		"retained", len(jobs)-len(recordsToRemove),
		"deleted", len(recordsToRemove),
		"limit", maxRecords,
	)

	return len(recordsToRemove), nil
}

// calculateRecordsToRemove determines which job records should be removed
// to stay within the specified limit.
func (w *History) calculateRecordsToRemove(jobs []*model.JobM, maxRecords int32) []string {
	if len(jobs) <= int(maxRecords) {
		return []string{}
	}

	// Sort jobs by creation time (oldest first for removal).
	sortedJobs := make([]*model.JobM, len(jobs))
	copy(sortedJobs, jobs)

	slices.SortFunc(sortedJobs, func(a, b *model.JobM) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})

	// Calculate how many records to remove.
	excessCount := len(sortedJobs) - int(maxRecords)
	recordsToRemove := make([]string, 0, excessCount)

	// Remove the oldest records.
	for i := 0; i < excessCount; i++ {
		recordsToRemove = append(recordsToRemove, sortedJobs[i].JobID)
	}

	return recordsToRemove
}

// Spec returns the cron specification for this watcher.
// The history watcher runs every second to perform cleanup operations.
func (w *History) Spec() string {
	return DefaultHistoryInterval
}

// SetStore sets the persistence store for the History watcher.
func (w *History) SetStore(store store.IStore) {
	w.store = store
}

// init registers the history watcher with the registry.
func init() {
	registry.Register(known.HistoryWatcher, &History{})
}
