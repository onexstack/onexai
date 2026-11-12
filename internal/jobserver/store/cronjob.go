package store

import "github.com/onexstack/onexai/internal/chat/store/jobstore"

// CronJobStore is the jobserver's view of the cronjob table. The single
// definition lives in chat's jobstore package, which owns the CRUD surface;
// this service consumes it to fire cronjobs asynchronously.
type CronJobStore = jobstore.CronJobStore

// CronJobExpansion is the cronjob store's extension point, aliased for callers
// that name it through this package.
type CronJobExpansion = jobstore.CronJobExpansion

// newCronJobStore hands the jobserver store's transaction-aware database
// provider to the shared implementation.
func newCronJobStore(s *store) CronJobStore { return jobstore.NewCronJobStore(s) }
