// Package cronjob implements a watcher for managing cron jobs.
// It provides functionality to monitor and schedule cron job executions
// based on database configurations.
package cronjob

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gammazero/workerpool"
	"github.com/onexstack/onexstack/pkg/ptr"
	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/onexstack/onexstack/pkg/watch"
	"github.com/onexstack/onexstack/pkg/watch/manager"
	"github.com/onexstack/onexstack/pkg/watch/registry"

	"github.com/onexstack/onexai/internal/jobserver/model"
	"github.com/onexstack/onexai/internal/jobserver/pkg/metrics"
	"github.com/onexstack/onexai/internal/jobserver/store"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	known "github.com/onexstack/onexai/pkg/known/job"
)

// Ensure Watcher implements the registry.Watcher interface.
var _ registry.Watcher = (*Watcher)(nil)

const (
	// CronJobPrefix is the prefix used for cron job names in the job manager.
	CronJobPrefix = "__oai_jobserver_cronjob/"

	// DefaultWatchInterval is the default interval for checking cron jobs.
	DefaultWatchInterval = "@every 1s"

	// JobNameTemplate is the template for generated job names.
	JobNameTemplate = "job-for-%s"
)

// Watcher implements a cron job watcher that monitors and schedules
// cron job executions based on database configurations.
type Watcher struct {
	watch.Watcher

	store store.IStore
	jm    *manager.JobManager
}

// NewWatcher creates a new Watcher instance with the provided dependencies.
func NewWatcher(store store.IStore, jm *manager.JobManager) *Watcher {
	return &Watcher{
		store: store,
		jm:    jm,
	}
}

// saveJob represents a job that saves cron job execution records to the database.
type saveJob struct {
	ctx     context.Context
	store   store.IStore
	cronJob *model.CronJobM
}

// Run executes the save job operation.
// It creates a new job record if the maximum job limit hasn't been reached.
func (j saveJob) Run() {
	// Check if we've reached the maximum number of jobs for this cron job.
	count, _, err := j.store.Job().List(j.ctx, where.F("cronjob_id", j.cronJob.CronJobID))
	if err != nil {
		return
	}

	if count >= known.MaxJobsPerCronJob {
		slog.Debug("maximum jobs per cron job reached", "cronjob_id", j.cronJob.CronJobID, "count", count, "max", known.MaxJobsPerCronJob)
		return
	}

	// Create a copy of the job template to avoid mutating the shared
	// CronJobM.JobTemplate, which this saveJob is repeatedly invoked with
	// on every scheduled run.
	job := ptr.Clone(j.cronJob.JobTemplate)
	job.ID = 0
	job.CronJobID = &j.cronJob.CronJobID
	job.Username = j.cronJob.Username
	job.Scope = j.cronJob.Scope
	job.Name = fmt.Sprintf(JobNameTemplate, j.cronJob.Name)
	job.CreatedAt = time.Now()

	if err := j.store.Job().Create(j.ctx, job); err != nil {
		return
	}

	slog.Info("created new job from cron job template", "job_name", job.Name, "cronjob_id", j.cronJob.CronJobID)
}

// Run executes the main watcher logic.
// It fetches active cron jobs from the database and manages their scheduling.
func (w *Watcher) Run() {
	ctx, cancel := context.WithTimeout(context.Background(), w.WatchTimeout)
	defer cancel()

	// Fetch all non-suspended cron jobs from the database.
	_, cronJobs, err := w.store.CronJob().List(ctx, where.F("suspend", known.JobNonSuspended))
	if err != nil {
		return
	}

	slog.Debug("fetched cron jobs from database", "count", len(cronJobs))

	// Remove cron jobs that no longer exist in the database.
	w.removeNonExistentCronJobs(cronJobs)

	wp := workerpool.New(w.PerConcurrency)
	for _, cronJob := range cronJobs {
		ctx := contextx.WithLogger(context.Background(), slog.With(
			"scope", cronJob.Scope,
			"cronjob_id", cronJob.CronJobID,
			"cronjob_name", cronJob.Name,
		))
		wp.Submit(func() { _ = w.processJob(ctx, cronJob) })
	}

	wp.StopWait()
}

// processJob processes a single job with error handling and rate limiting.
func (w *Watcher) processJob(ctx context.Context, cronJob *model.CronJobM) (err error) {
	w.Limiter.Take()

	metrics.M.RecordCronJobExecution(ctx, cronJob.CronJobID)
	defer func() {
		if err != nil {
			metrics.M.RecordCronJobFailure(ctx, cronJob.CronJobID, err.Error())
		}
	}()

	jobName := cronJobName(cronJob.CronJobID)

	// Validate cron job configuration.
	if cronJob.JobTemplate == nil {
		slog.Warn("skipping cron job with nil job template")
		return nil
	}

	// Skip if already scheduled.
	if w.jm.Exists(jobName) {
		slog.Debug("cron job already scheduled")
		return nil
	}

	w.jm.Add(jobName, cronJob.Schedule, saveJob{store: w.store, ctx: ctx, cronJob: cronJob})

	slog.Debug("added cron job to scheduler", "schedule", cronJob.Schedule)

	return nil
}

// removeNonExistentCronJobs removes cron jobs from the scheduler that no longer exist in the database.
func (w *Watcher) removeNonExistentCronJobs(cronJobs []*model.CronJobM) {
	validCronJobIDs := make(map[string]struct{}, len(cronJobs))
	for _, cronjob := range cronJobs {
		validCronJobIDs[cronJobName(cronjob.CronJobID)] = struct{}{}
	}

	removedCount := 0
	for jobName := range w.jm.GetJobs() {
		if !isCronJobName(jobName) {
			continue
		}

		if _, exists := validCronJobIDs[jobName]; exists {
			continue
		}

		if err := w.jm.Del(jobName); err != nil {
			slog.Error("failed to remove cron job from scheduler", "job_name", jobName, "error", err)
			continue
		}

		removedCount++
		slog.Info("removed non-existent cron job from scheduler", "job_name", jobName)
	}

	if removedCount > 0 {
		slog.Info("cleanup completed", "removed_jobs", removedCount)
	}
}

// Spec returns the cron specification for this watcher.
// The watcher runs every second to check for cron job updates.
func (w *Watcher) Spec() string {
	return DefaultWatchInterval
}

// SetStore sets the persistence store for the Watcher.
func (w *Watcher) SetStore(store store.IStore) {
	w.store = store
}

// SetJobManager sets the JobManager for the Watcher.
func (w *Watcher) SetJobManager(jm *manager.JobManager) {
	w.jm = jm
}

// cronJobName generates a job name for the given cron job ID.
func cronJobName(cronJobID string) string {
	return fmt.Sprintf("%s%s", CronJobPrefix, cronJobID)
}

// isCronJobName checks if the given job name belongs to our cron job namespace.
func isCronJobName(jobName string) bool {
	return strings.HasPrefix(jobName, CronJobPrefix)
}

// init registers the watcher with the registry.
func init() {
	registry.Register(known.CronJobWatcher, &Watcher{})
}
