package llmtrain

import (
	"context"
	"log/slog"

	"github.com/gammazero/workerpool"
	"github.com/onexstack/onexstack/pkg/store/where"
	"github.com/onexstack/onexstack/pkg/watch"
	"github.com/onexstack/onexstack/pkg/watch/registry"
	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms/ollama"

	"github.com/onexstack/onexai/internal/jobserver/model"
	"github.com/onexstack/onexai/internal/jobserver/pkg/clientset/typed/minio"
	"github.com/onexstack/onexai/internal/jobserver/pkg/clientset/typed/train"
	"github.com/onexstack/onexai/internal/jobserver/pkg/metrics"
	"github.com/onexstack/onexai/internal/jobserver/store"
	"github.com/onexstack/onexai/internal/jobserver/watcher"
	"github.com/onexstack/onexai/internal/pkg/contextx"
	fakeembedder "github.com/onexstack/onexai/internal/pkg/embedding/embedder/fake"
	known "github.com/onexstack/onexai/pkg/known/job"
)

// runnablePhases defines the list of job statuses that the watcher is interested in processing.
// Defined as a package-level variable to avoid reallocation on every Run cycle.
var runnablePhases = []string{
	known.LLMTrainPending,
	known.LLMTrainDownloading,
	known.LLMTrainDownloaded,
	known.LLMTrainEmbedding,
	known.LLMTrainEmbedded,
	known.LLMTrainTraining,
	known.LLMTrainTrained,
}

// Ensure Watcher implements the registry.Watcher interface.
var _ registry.Watcher = (*Watcher)(nil)

// Watcher monitors and processes daily estimation jobs.
type Watcher struct {
	watch.Watcher

	Minio minio.Interface
	Store store.IStore

	embedder embeddings.Embedder
	train    train.Interface
}

// Run executes the watcher logic to process jobs.
// It retrieves runnable jobs from the store and processes them concurrently using a worker pool.
func (w *Watcher) Run() {
	_, jobs, err := w.Store.Job().List(context.Background(), where.F(
		"scope", known.LLMJobScope,
		"watcher", known.LLMTrainWatcher,
		"status", runnablePhases,
		"suspend", known.JobNonSuspended,
	))
	if err != nil {
		return
	}

	if len(jobs) == 0 {
		return
	}

	wp := workerpool.New(w.PerConcurrency)
	for _, job := range jobs {
		ctx := contextx.WithLogger(context.Background(), slog.With(
			"scope", job.Scope,
			"cronjob_id", job.CronJobID,
			"watcher", job.Watcher,
			"job_id", job.JobID,
			"job_name", job.Name,
		))

		wp.Submit(func() {
			// Ignore the error here as it's already logged inside processJob.
			_ = w.processJob(ctx, job)
		})
	}

	wp.StopWait()
}

// processJob processes a single job with error handling and rate limiting.
func (w *Watcher) processJob(ctx context.Context, jobM *model.JobM) (err error) {
	w.Limiter.Take()

	metrics.M.RecordJobExecution(ctx, "job", jobM.JobID)
	defer func() {
		if err != nil {
			metrics.M.RecordJobFailure(ctx, "job", jobM.JobID, err.Error())
		}
	}()

	logger := contextx.L(ctx)
	logger.InfoContext(ctx, "Started to process job")

	sm := NewStateMachine(jobM.Status, w, jobM)
	if err := sm.FSM.Event(ctx, jobM.Status); err != nil {
		return err
	}

	logger.InfoContext(ctx, "Ended to process job")

	return nil
}

// Spec returns the cron job specification for scheduling.
func (w *Watcher) Spec() string {
	return "@every 1s"
}

// SetAggregateConfig configures the watcher with the provided aggregate configuration.
func (w *Watcher) SetAggregateConfig(config *watcher.AggregateConfig) {
	w.Minio = config.Minio
	w.Store = config.Store

	// Ideally, these should be configured via config, not hardcoded.
	w.embedder = fakeembedder.NewFakeEmbedder(3)
	w.train = train.NewForConfig()
}

// NewLlamaEmbedder initializes a new Ollama-based embedder.
// Returns an error if client creation fails, pushing logging responsibility to the caller.
func NewLlamaEmbedder() (embeddings.Embedder, error) {
	llm, err := ollama.New(ollama.WithModel("llama3"))
	if err != nil {
		return nil, err
	}

	embedder, err := embeddings.NewEmbedder(llm)
	if err != nil {
		return nil, err
	}

	return embedder, nil
}

func init() {
	registry.Register(known.LLMTrainWatcher, &Watcher{})
}
