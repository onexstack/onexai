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

package model

// ScheduledRunM is the REST-facing view of one schedule. It is no longer a
// database row: the ScheduledRun facade reads and writes a batch.onex.io
// CronJob, and this type is the shape the existing API and conversion helpers
// speak.
type ScheduledRunM struct {
	ID          uint64 `json:"id"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Owner       string `json:"owner"`
	// CreatedBy is the admission annotation; it is the fallback subject when
	// spec.owner is empty.
	CreatedBy string `json:"created_by"`

	Agent                 string `json:"agent"`
	Schedule              string `json:"schedule"`
	TimeZone              string `json:"time_zone"`
	Prompt                string `json:"prompt"`
	Paused                bool   `json:"paused"`
	ExecutionTimeoutNanos int64  `json:"execution_timeout_nanos"`

	LastExecutionID string `json:"last_execution_id"`
	LastScheduledAt int64  `json:"last_scheduled_at"`
	// NextScheduledAt is computed on read from schedule/timeZone/paused; it is
	// never persisted and never accepted from a caller.
	NextScheduledAt int64 `json:"next_scheduled_at"`

	Labels    StringMap `json:"labels"`
	CreatedAt int64     `json:"created_at"`
	UpdatedAt int64     `json:"updated_at"`
}

// Execution states.
const (
	// ExecutionStatePending means the Job exists but the controller has not
	// started it yet. Without it a not-yet-started Job would read as running,
	// which hides a controller that is down or failing to dispatch.
	ExecutionStatePending   = "pending"
	ExecutionStateRunning   = "running"
	ExecutionStateCompleted = "completed"
	ExecutionStateFailed    = "failed"
	ExecutionStateCanceled  = "canceled"
)

// ScheduledExecutionM is one occurrence. It is the REST-facing view of a child
// Job; the Job controller owns the durable row.
type ScheduledExecutionM struct {
	ID            string `json:"id"`
	Namespace     string `json:"namespace"`
	Schedule      string `json:"schedule"`
	Trigger       string `json:"trigger"` // scheduled | manual
	Prompt        string `json:"prompt"`
	SessionID     string `json:"session_id"`
	RunID         string `json:"run_id"`
	State         string `json:"state"`
	FailureReason string `json:"failure_reason"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    int64  `json:"finished_at"`
	CreatedAt     int64  `json:"created_at"`
}
