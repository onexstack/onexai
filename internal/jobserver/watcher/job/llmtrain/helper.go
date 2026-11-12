package llmtrain

import (
	"time"

	"github.com/onexstack/onexai/internal/jobserver/model"
	jobconditionsutil "github.com/onexstack/onexai/internal/pkg/util/jobconditions"
	known "github.com/onexstack/onexai/pkg/known/job"
)

// isJobTimeout checks if the job has exceeded its allowed execution time.
// It uses the configured timeout from job parameters or falls back to the default global timeout.
func isJobTimeout(job *model.JobM) bool {
	if job.StartedAt == nil {
		return false
	}

	elapsed := time.Since(*job.StartedAt)

	timeoutSeconds := job.Params.Train.JobTimeout
	if timeoutSeconds == 0 {
		timeoutSeconds = int64(known.LLMTrainTimeout)
	}

	return elapsed > time.Duration(timeoutSeconds)*time.Second
}

// ShouldSkipOnIdempotency determines whether a job should skip execution based on idempotency rules.
// It returns false if idempotency is disabled for the job.
func ShouldSkipOnIdempotency(job *model.JobM, conditionType string) bool {
	// If idempotent execution is not enabled, proceed with execution.
	if job.Params.Train.IdempotentExecution != known.IdempotentExecution {
		return false
	}

	return jobconditionsutil.IsTrue(job.Conditions, conditionType)
}

// SetDefaultJobParams applies default configuration to the job parameters.
// This ensures that critical fields like JobTimeout have valid values before processing.
func SetDefaultJobParams(job *model.JobM) {
	if job.Params.Train.JobTimeout == 0 {
		job.Params.Train.JobTimeout = int64(known.LLMTrainTimeout)
	}
}
