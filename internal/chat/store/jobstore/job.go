// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package jobstore is the single definition of the job and cronjob stores.
//
// It lives under chat because chat is the surface that creates and edits these
// records; oai-jobserver consumes the same stores to execute them
// asynchronously. Both services construct them over their own database
// provider, so there is one implementation rather than a copy per service.
package jobstore

import (
	"context"

	storelogger "github.com/onexstack/onexstack/pkg/logger/slog/store"
	genericstore "github.com/onexstack/onexstack/pkg/store"
	"github.com/onexstack/onexstack/pkg/store/where"

	jobmodel "github.com/onexstack/onexai/internal/jobserver/model"
)

// JobStore defines the interface for managing job-related data operations. It
// reuses the jobserver model (internal/jobserver/model.JobM) so the chat
// commands and oai-jobserver's watcher operate on the same job.job table.
type JobStore interface {
	// Create inserts a new Job record into the store.
	Create(ctx context.Context, obj *jobmodel.JobM) error

	// Update modifies an existing Job record in the store based on the given model.
	Update(ctx context.Context, obj *jobmodel.JobM) error

	// Delete removes Job records that satisfy the given query options.
	Delete(ctx context.Context, opts *where.Options) error

	// Get retrieves a single Job record that satisfies the given query options.
	Get(ctx context.Context, opts *where.Options) (*jobmodel.JobM, error)

	// List retrieves a list of Job records and their total count based on the given query options.
	List(ctx context.Context, opts *where.Options) (int64, []*jobmodel.JobM, error)

	// JobExpansion is a placeholder for extension methods for jobs,
	// to be implemented by additional interfaces if needed.
	JobExpansion
}

// JobExpansion is an empty interface provided for extending
// the JobStore interface.
type JobExpansion interface{}

// jobStore implements the JobStore interface over the generic onexstack store.
type jobStore struct {
	*genericstore.Store[jobmodel.JobM]
}

// Ensure jobStore satisfies the JobStore interface at compile time.
var _ JobStore = (*jobStore)(nil)

// NewJobStore creates a JobStore over the given database provider. A provider
// is enough: the generic store only needs DB(ctx, ...where.Where), which both
// the chat and jobserver stores already implement with their transaction-aware
// context.
func NewJobStore(p genericstore.DBProvider) JobStore {
	return &jobStore{Store: genericstore.NewStore[jobmodel.JobM](p, storelogger.NewLogger())}
}
