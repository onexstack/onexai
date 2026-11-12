// Package statesync implements a watcher for synchronizing cron job states.
// It monitors and updates the status information of cron jobs based on their
// associated job execution history and current active job states.
package statesync

import (
	"context"
	"log/slog"

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

const (
	// DefaultWatchInterval is the default interval for checking jobs.
	DefaultWatchInterval = "@every 1s"
)

// Ensure Watcher implements the registry.Watcher interface.
var _ registry.Watcher = (*Watcher)(nil)

// Watcher implements a state synchronization watcher that updates cron job statuses
// based on their associated job execution states. It processes multiple cron jobs
// concurrently using a worker pool for better performance.
type Watcher struct {
	watch.Watcher

	store store.IStore // Database store interface for accessing cron jobs and jobs
}

// Run executes the state synchronization process for all active cron jobs.
// It fetches all non-suspended cron jobs and updates their status information
// based on their associated job execution history using concurrent processing.
func (w *Watcher) Run() {
	ctx := context.Background()

	slog.Info("starting cron job state synchronization")

	// Query active cron jobs that are not suspended
	_, cronjobs, err := w.store.CronJob().List(ctx, where.F("suspend", 0))
	if err != nil {
		return
	}

	slog.Info("retrieved cron jobs for state sync", "count", len(cronjobs))

	wp := workerpool.New(w.PerConcurrency)

	// Process each cron job in a separate worker
	for _, cronJob := range cronjobs {
		ctx := contextx.WithLogger(context.Background(), slog.With("cronjob_id", cronJob.CronJobID))
		wp.Submit(func() { _ = w.processJob(ctx, cronJob) })
	}

	// Wait for all workers to complete their tasks
	wp.StopWait()

	slog.Info("cron job state synchronization completed")
}

// processJob processes a single job with error handling and rate limiting.
func (w *Watcher) processJob(ctx context.Context, cronJob *model.CronJobM) (err error) {
	w.Limiter.Take()

	contextx.L(ctx).DebugContext(ctx, "processing cron job state sync")
	metrics.M.RecordCronJobExecution(ctx, cronJob.CronJobID)
	defer func() {
		if err != nil {
			metrics.M.RecordCronJobFailure(ctx, cronJob.CronJobID, err.Error())
		}
	}()

	// Fetch all jobs associated with this cron job
	_, jobs, err := w.store.Job().List(ctx, where.F("cronjob_id", cronJob.CronJobID))
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		contextx.L(ctx).DebugContext(ctx, "no jobs found for cron job")
		return nil
	}

	contextx.L(ctx).DebugContext(ctx, "retrieved jobs for cron job", "jobCount", len(jobs))

	// Initialize collections for tracking job states
	active := make([]string, 0)
	var lastSuccessJob *model.JobM
	var lastScheduleJob *model.JobM

	// Process each job related to the cron job
	for _, job := range jobs {
		// Collect active (running) jobs
		if job.Status == known.JobRunning {
			active = append(active, job.JobID)
		}

		// QueryJobM orders by ID in descending order, with the first being the most recent.
		// Find the most recent successful job
		if lastSuccessJob == nil && job.Status == known.JobSucceeded {
			lastSuccessJob = job
		}

		// Find the most recent scheduled job (job that has been started)
		if lastScheduleJob == nil && job.StartedAt != nil {
			lastScheduleJob = job
		}
	}

	// Build the updated status for the cron job
	cronJob.Status = model.CronJobStatus{Actives: active, LastJobID: jobs[0].JobID}

	// Set the last successful execution time if available
	if lastSuccessJob != nil && lastSuccessJob.EndedAt != nil {
		cronJob.Status.LastSuccessfulTime = lastSuccessJob.EndedAt.Unix()
		contextx.L(ctx).DebugContext(ctx, "updated last successful time", "time", lastSuccessJob.EndedAt)
	}

	// Set the last schedule time if available
	if lastScheduleJob != nil && lastScheduleJob.StartedAt != nil {
		cronJob.Status.LastScheduleTime = lastScheduleJob.StartedAt.Unix()
		contextx.L(ctx).DebugContext(ctx, "updated last schedule time", "schedule_time", lastScheduleJob.StartedAt.Format("2006-01-02 15:04:05"))
	}

	// Update the cron job status in the database
	_ = w.store.CronJob().Update(ctx, cronJob)

	return nil
}

// Spec returns the cron specification for this watcher.
// The state sync watcher runs every second to keep cron job states up to date.
func (w *Watcher) Spec() string {
	return DefaultWatchInterval
}

// SetStore sets the persistence store for the Watcher.
// This store is used to access and update cron job and job data.
func (w *Watcher) SetStore(store store.IStore) {
	w.store = store
}

// init registers the state sync watcher with the registry.
func init() {
	registry.Register(known.StateSyncWatcher, &Watcher{})
}
