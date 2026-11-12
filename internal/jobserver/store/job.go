package store

import "github.com/onexstack/onexai/internal/chat/store/jobstore"

// JobStore is the jobserver's view of the job table. The single definition
// lives in chat's jobstore package, which owns the CRUD surface; this service
// consumes it to execute jobs asynchronously.
type JobStore = jobstore.JobStore

// JobExpansion is the job store's extension point, aliased for callers that
// name it through this package.
type JobExpansion = jobstore.JobExpansion

// newJobStore hands the jobserver store's transaction-aware database provider
// to the shared implementation.
func newJobStore(s *store) JobStore { return jobstore.NewJobStore(s) }
