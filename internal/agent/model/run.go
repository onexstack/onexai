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

import (
	"github.com/onexstack/onexstack/pkg/store/registry"

	"github.com/onexstack/onexai/pkg/dbschema"
)

func init() {
	registry.Register(&SessionM{})
	registry.Register(&SessionShareM{})
	registry.Register(&SessionMessageM{})
	registry.Register(&RevisionM{})
	registry.Register(&RunM{})
	registry.Register(&RunAdmissionM{})
	registry.Register(&RunEventM{})
	registry.Register(&CheckpointM{})
	registry.Register(&ApprovalM{})
	registry.Register(&RuntimeInstanceM{})
}

// Session states.
const (
	SessionStateReady    = "ready"
	SessionStateDeleting = "deleting"
	SessionStateDeleted  = "deleted"
)

// Run states.
const (
	RunStatePending     = "pending"
	RunStateRunning     = "running"
	RunStateCompleted   = "completed"
	RunStateSucceeded   = "succeeded"
	RunStateFailed      = "failed"
	RunStateCanceled    = "canceled"
	RunStateInterrupted = "interrupted"
)

// RunAdmissionM is the durable, idempotent admission of an asynchronous run.
// A caller submits a dedup key; at most one row exists for it, so a retried
// dispatch cannot start the same occurrence twice. The RunM row itself is
// created by AgentBiz.Ask once execution starts.
type RunAdmissionM struct {
	ID        string `gorm:"primaryKey;size:128" json:"id"`
	DedupKey  string `gorm:"size:128;uniqueIndex" json:"dedup_key"`
	SessionID string `gorm:"size:128;index" json:"session_id"`
	Subject   string `gorm:"size:128;index" json:"subject"`
	// Agent is the agent the run was admitted for. It is denormalized from the
	// request payload so the management list can filter by agent without
	// joining sessions (a pending admission has no session row yet).
	Agent         string `gorm:"size:128;index;index:idx_run_admission_agent_created,priority:1" json:"agent"`
	Trigger       string `gorm:"size:32;index" json:"trigger"`
	Source        string `gorm:"size:64;index" json:"source"`
	ScheduleRef   string `gorm:"size:256;index" json:"schedule_ref"`
	ScheduledTime int64  `gorm:"index:idx_run_admission_due,priority:2" json:"scheduled_time"`
	// Attempt is the execution number carried by the caller (the job
	// controller sets it when it retries a scheduled run). ClaimCount is this
	// row's own claim counter; they answer different questions and must not
	// share a column.
	Attempt int32 `json:"attempt"`
	// LeaseOwner identifies one claim, not one process: it is a fresh UUID per
	// claim, so two attempts inside the same pod are still distinguishable.
	LeaseOwner string `gorm:"size:64" json:"lease_owner"`
	// LeaseUntil is the claim's expiry in Unix milliseconds, written from the
	// database clock. A zero value marks a row claimed before leases existed
	// or completed by a process that never renewed, and is therefore
	// immediately reclaimable.
	LeaseUntil  int64  `gorm:"not null;default:0;index:idx_run_admission_lease,priority:2" json:"lease_until"`
	ClaimCount  int32  `gorm:"not null;default:0" json:"claim_count"`
	State       string `gorm:"size:32;index;index:idx_run_admission_due,priority:1;index:idx_run_admission_lease,priority:1" json:"state"`
	RequestJSON string `gorm:"type:text" json:"request_json"`
	Message     string `gorm:"type:text" json:"message"`
	CreatedAt   int64  `gorm:"index;index:idx_run_admission_agent_created,priority:2" json:"created_at"`
	UpdatedAt   int64  `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the run admission table name.
func (RunAdmissionM) TableName() string { return dbschema.Table(dbschema.Agent, "run_admission") }

// SessionM is the durable identity of one conversation. It pins the runtime,
// profile and revision it started on: changing any of them requires a new
// session, because the runtime's context/task state belongs to that triple.
type SessionM struct {
	ID              string `gorm:"primaryKey;size:128" json:"id"`
	Namespace       string `gorm:"column:namespace;size:128;index" json:"namespace"`
	Agent           string `gorm:"size:128;index" json:"agent"`
	RuntimeKind     string `gorm:"size:64" json:"runtime_kind"`
	RuntimeProfile  string `gorm:"size:128" json:"runtime_profile"`
	RuntimeInstance string `gorm:"size:128;index" json:"runtime_instance"`
	RevisionID      string `gorm:"size:64" json:"revision_id"`
	Owner           string `gorm:"size:128;index" json:"owner"`
	Name            string `gorm:"size:200" json:"name"`
	State           string `gorm:"size:32;index" json:"state"`
	LastActiveAt    int64  `gorm:"index" json:"last_active_at"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `gorm:"autoUpdateTime:milli" json:"updated_at"`
	DeletedAt       int64  `gorm:"index" json:"deleted_at"`
}

// TableName returns the session table name.
func (SessionM) TableName() string { return dbschema.Table(dbschema.Agent, "session") }

// SessionMessageM is one conversation message. It is the durable history the
// runtime loads; Redis may cache it, but this row is the source.
type SessionMessageM struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID string `gorm:"size:128;index:idx_session_message_seq,priority:1" json:"session_id"`
	Seq       int64  `gorm:"index:idx_session_message_seq,priority:2" json:"seq"`
	Role      string `gorm:"size:16" json:"role"`
	Content   string `gorm:"type:text" json:"content"`
	Images    string `gorm:"type:text" json:"images"`
	CreatedAt int64  `json:"created_at"`
}

// TableName returns the session message table name.
func (SessionMessageM) TableName() string { return dbschema.Table(dbschema.Agent, "session_message") }

// SessionShareM is a read-only grant on a session.
type SessionShareM struct {
	ID        string `gorm:"primaryKey;size:64" json:"id"`
	SessionID string `gorm:"size:128;index" json:"session_id"`
	TokenHash string `gorm:"size:64;index" json:"token_hash"`
	CreatedBy string `gorm:"size:128" json:"created_by"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `gorm:"index" json:"expires_at"`
}

// TableName returns the session share table name.
func (SessionShareM) TableName() string { return dbschema.Table(dbschema.Agent, "session_share") }

// RevisionM is an immutable compiled runtime input. The digest is over every
// input that can change behaviour; two rows with the same digest must be
// interchangeable.
type RevisionM struct {
	ID         string `gorm:"primaryKey;size:128" json:"id"`
	Namespace  string `gorm:"column:namespace;size:128;index" json:"namespace"`
	Agent      string `gorm:"size:128;index" json:"agent"`
	Generation int64  `json:"generation"`
	Digest     string `gorm:"size:64;index" json:"digest"`
	SpecJSON   string `gorm:"type:text" json:"spec_json"`
	CreatedAt  int64  `json:"created_at"`
}

// TableName returns the revision table name.
func (RevisionM) TableName() string { return dbschema.Table(dbschema.Agent, "revision") }

// RunM is one turn on a session.
type RunM struct {
	ID         string `gorm:"primaryKey;size:128" json:"id"`
	SessionID  string `gorm:"size:128;index" json:"session_id"`
	RevisionID string `gorm:"size:64" json:"revision_id"`
	Subject    string `gorm:"size:128;index" json:"subject"`
	Actor      string `gorm:"size:128" json:"actor"`
	Runtime    string `gorm:"size:64" json:"runtime"`
	TaskID     string `gorm:"size:128;index" json:"task_id"`
	RequestID  string `gorm:"size:128;index" json:"request_id"`
	State      string `gorm:"size:32;index" json:"state"`
	Error      string `gorm:"type:text" json:"error"`
	UsageJSON  string `gorm:"type:text" json:"usage_json"`
	// TaskJSON is the A2A task snapshot a remote runtime persists through the
	// task store. Empty for an in-process run.
	TaskJSON   string `gorm:"type:text" json:"task_json"`
	Version    int64  `json:"version"`
	StartedAt  int64  `gorm:"index" json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the run table name.
func (RunM) TableName() string { return dbschema.Table(dbschema.Agent, "run") }

// RunEventM is one semantic event on a run. Token deltas are deliberately not
// stored: they are transport, not history.
type RunEventM struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	RunID     string `gorm:"size:64;index:idx_run_event_seq,priority:1" json:"run_id"`
	Seq       int64  `gorm:"index:idx_run_event_seq,priority:2" json:"seq"`
	Type      string `gorm:"size:32" json:"type"`
	Payload   string `gorm:"type:text" json:"payload"`
	CreatedAt int64  `json:"created_at"`
}

// TableName returns the run event table name.
func (RunEventM) TableName() string { return dbschema.Table(dbschema.Agent, "run_event") }

// CheckpointM is the durable metadata of a paused run. The state bytes live in
// object storage (or the existing Redis checkpoint cache); this row is the
// authoritative index.
type CheckpointM struct {
	ID        string `gorm:"primaryKey;size:64" json:"id"`
	SessionID string `gorm:"size:128;index" json:"session_id"`
	RunID     string `gorm:"size:128;index" json:"run_id"`
	HeadRunID string `gorm:"size:64" json:"head_run_id"`
	StateRef  string `gorm:"size:1024" json:"state_ref"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `gorm:"index" json:"expires_at"`
}

// TableName returns the checkpoint table name.
func (CheckpointM) TableName() string { return dbschema.Table(dbschema.Agent, "checkpoint") }

// ApprovalM is one pending human decision.
type ApprovalM struct {
	ID        string `gorm:"primaryKey;size:64" json:"id"`
	RunID     string `gorm:"size:128;index" json:"run_id"`
	RequestID string `gorm:"size:128;index" json:"request_id"`
	SessionID string `gorm:"size:128;index" json:"session_id"`
	// Subject is the principal the paused run belongs to. It is recorded on the
	// approval rather than looked up through the session so the approval centre
	// can be filtered without a join, and so a deleted session does not orphan
	// the question.
	Subject   string `gorm:"size:128;index" json:"subject"`
	Agent     string `gorm:"size:128" json:"agent"`
	Tool      string `gorm:"size:256" json:"tool"`
	Args      string `gorm:"type:text" json:"args"`
	Status    string `gorm:"size:32;index" json:"status"`
	Reason    string `gorm:"type:text" json:"reason"`
	ExpiresAt int64  `gorm:"index" json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the approval table name.
func (ApprovalM) TableName() string { return dbschema.Table(dbschema.Agent, "approval") }

// Runtime instance states.
const (
	InstanceStateStarting = "starting"
	InstanceStateReady    = "ready"
	InstanceStateDraining = "draining"
	InstanceStateStopped  = "stopped"
)

// RuntimeInstanceM is one hosting-pool instance. A session is pinned to the
// instance it started on, so this row is the routing table.
type RuntimeInstanceM struct {
	ID         string `gorm:"primaryKey;size:128" json:"id"`
	Namespace  string `gorm:"column:namespace;size:128;index" json:"namespace"`
	Profile    string `gorm:"size:128;index" json:"profile"`
	Agent      string `gorm:"size:128;index" json:"agent"`
	Endpoint   string `gorm:"size:1024" json:"endpoint"`
	State      string `gorm:"size:32;index" json:"state"`
	LeaseUntil int64  `gorm:"index" json:"lease_until"`
	LastUsedAt int64  `gorm:"index" json:"last_used_at"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `gorm:"autoUpdateTime:milli" json:"updated_at"`
}

// TableName returns the runtime instance table name.
func (RuntimeInstanceM) TableName() string { return dbschema.Table(dbschema.Agent, "runtime_instance") }
