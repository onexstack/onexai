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

package jobstore

import (
	"context"

	storelogger "github.com/onexstack/onexstack/pkg/logger/slog/store"
	genericstore "github.com/onexstack/onexstack/pkg/store"
	"github.com/onexstack/onexstack/pkg/store/where"

	jobmodel "github.com/onexstack/onexai/internal/jobserver/model"
)

// CronJobStore defines the interface for managing cronjob-related data
// operations. It reuses the jobserver model (internal/jobserver/model.CronJobM)
// so the chat commands and oai-jobserver's cronjob watcher operate on the same
// job.cronjob table.
type CronJobStore interface {
	// Create inserts a new CronJob record into the store.
	Create(ctx context.Context, obj *jobmodel.CronJobM) error

	// Update modifies an existing CronJob record in the store based on the given model.
	Update(ctx context.Context, obj *jobmodel.CronJobM) error

	// Delete removes CronJob records that satisfy the given query options.
	Delete(ctx context.Context, opts *where.Options) error

	// Get retrieves a single CronJob record that satisfies the given query options.
	Get(ctx context.Context, opts *where.Options) (*jobmodel.CronJobM, error)

	// List retrieves a list of CronJob records and their total count based on the given query options.
	List(ctx context.Context, opts *where.Options) (int64, []*jobmodel.CronJobM, error)

	// CronJobExpansion is a placeholder for extension methods for cronjobs,
	// to be implemented by additional interfaces if needed.
	CronJobExpansion
}

// CronJobExpansion is an empty interface provided for extending
// the CronJobStore interface.
type CronJobExpansion interface{}

// cronJobStore implements the CronJobStore interface over the generic onexstack
// store.
type cronJobStore struct {
	*genericstore.Store[jobmodel.CronJobM]
}

// Ensure cronJobStore satisfies the CronJobStore interface at compile time.
var _ CronJobStore = (*cronJobStore)(nil)

// NewCronJobStore creates a CronJobStore over the given database provider.
func NewCronJobStore(p genericstore.DBProvider) CronJobStore {
	return &cronJobStore{Store: genericstore.NewStore[jobmodel.CronJobM](p, storelogger.NewLogger())}
}
