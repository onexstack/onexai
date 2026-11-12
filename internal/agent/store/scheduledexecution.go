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

package store

import (
	"context"

	"github.com/onexstack/onexai/internal/agent/model"
)

// ScheduledExecutionStore is the occurrence history behind a ScheduledRun.
// The batch implementation lists the Jobs a CronJob owns and creates a manual
// occurrence as a child Job, so the REST facade never needs the old scheduler.
type ScheduledExecutionStore interface {
	ListExecution(ctx context.Context, namespace, schedule string, limit int) ([]*model.ScheduledExecutionM, error)
	GetExecution(ctx context.Context, id string) (*model.ScheduledExecutionM, error)
	CreateManualExecution(ctx context.Context, namespace, schedule string) (*model.ScheduledExecutionM, error)
	CancelExecution(ctx context.Context, id string) error
}
